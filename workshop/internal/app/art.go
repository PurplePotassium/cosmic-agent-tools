package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/chroma"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/engine"
)

// ArtStatus is the dashboard/API view of the art-generation settings: which
// green/blue-screen keyers art-gen-trans runs (live-switchable, ordered,
// primary first) and what the agy model verification found.
type ArtStatus struct {
	// Removers is every selectable backend name.
	Removers []string `json:"removers"`
	// Keyers is the EFFECTIVE ordered list (override over config): the first
	// entry's output becomes the committed asset, the rest key archived
	// comparison copies.
	Keyers []string `json:"keyers"`
	// Remover is the effective primary keyer — Keyers[0], kept as its own
	// field for one-glance display.
	Remover string `json:"remover"`
	// Override is the live keyer-list override (nil = using config).
	Override []string `json:"override,omitempty"`
	// Configured is the [art] config-resolved list.
	Configured []string `json:"configured"`
	// Model is the launch-verified agy label art passes hand to agy ("" =
	// probe has not succeeded; passes then assume the preferred default).
	Model string `json:"model,omitempty"`
	// AgyModels is every label the last successful probe saw.
	AgyModels []string `json:"agyModels,omitempty"`
	// Wanted is the ordered preference list of allowed art models.
	Wanted []string `json:"wanted"`
	// Verifying reports an agy model verification in flight (launch probe or
	// dashboard re-verify) so the settings panel can show progress.
	Verifying bool `json:"verifying,omitempty"`
}

// ArtStatus assembles the current art settings view.
func (a *App) ArtStatus(ctx context.Context) ArtStatus {
	st := ArtStatus{
		Removers:   chroma.Removers,
		Configured: a.Res().Config.ArtKeyers(),
		Wanted:     domain.ArtAgyModels,
		Verifying:  a.artVerifying.Load(),
	}
	st.Keyers = st.Configured
	if o := a.artKeyerOverride(ctx); len(o) > 0 {
		st.Override = o
		st.Keyers = o
	}
	st.Remover = st.Keyers[0]
	st.Model, _ = a.Store.GetKV(ctx, engine.KVArtAgyModel)
	if raw, err := a.Store.GetKV(ctx, engine.KVArtAgyModels); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &st.AgyModels)
	}
	return st
}

// artKeyerOverride reads the live keyer-list override: the JSON-array kv
// (settings panel) first, falling back to the legacy single-value kv a
// pre-multi-keyer build may have left behind. nil = no override.
func (a *App) artKeyerOverride(ctx context.Context) []string {
	if raw, err := a.Store.GetKV(ctx, engine.KVArtRemovers); err == nil && raw != "" {
		var list []string
		if json.Unmarshal([]byte(raw), &list) == nil && len(list) > 0 {
			return list
		}
	}
	if v, err := a.Store.GetKV(ctx, engine.KVArtRemover); err == nil && v != "" {
		return []string{v}
	}
	return nil
}

// SetArtKeyers sets (or, for an empty list, clears) the live keyer-list
// override: ordered, primary first, every entry a selectable backend, no
// duplicates. Art passes re-read it every pass, so it takes effect on the
// NEXT art-gen-trans pass without a restart — the art counterpart of the
// pipeline bundle/mode/personality overrides.
func (a *App) SetArtKeyers(ctx context.Context, keyers []string) error {
	seen := map[string]bool{}
	for _, k := range keyers {
		if k == "" || !chroma.ValidRemover(k) {
			return fmt.Errorf("keyer %q is not one of %v", k, chroma.Removers)
		}
		if seen[k] {
			return fmt.Errorf("keyer %q listed twice", k)
		}
		seen[k] = true
	}
	value := ""
	if len(keyers) > 0 {
		raw, err := json.Marshal(keyers)
		if err != nil {
			return err
		}
		value = string(raw)
	}
	if err := a.Store.SetKV(ctx, engine.KVArtRemovers, value); err != nil {
		return err
	}
	// Retire any legacy single-value override so the two keys can never
	// disagree about what "the override" is.
	if err := a.Store.SetKV(ctx, engine.KVArtRemover, ""); err != nil {
		return err
	}
	a.Bus.Publish(ctx, domain.Event{Type: "art.remover", Payload: map[string]any{
		"keyers": keyers, "cleared": len(keyers) == 0,
	}})
	return nil
}

// SetArtRemover is the legacy single-keyer form of SetArtKeyers ("" clears),
// kept for older callers of PUT /api/v1/art/remover.
func (a *App) SetArtRemover(ctx context.Context, remover string) error {
	if remover == "" {
		return a.SetArtKeyers(ctx, nil)
	}
	return a.SetArtKeyers(ctx, []string{remover})
}

// artVerifyOnce dedupes the launch probe across engine relaunches (dashboard
// halt/resume restarts RunHeadless within the same process).
var artVerifyOnce sync.Once

// VerifyArtModelsAsync launches the agy art-model verification in the
// background — once per process, and never in test harnesses (the fake-agent
// env) where spawning a real agy would hit the network and pollute agy's
// history. agyMu is the engine's agy serialization lock (nil = unlocked).
func (a *App) VerifyArtModelsAsync(ctx context.Context, agyMu *sync.RWMutex) {
	if os.Getenv("WORKSHOP_FAKE_BIN") != "" || truthy(os.Getenv("WORKSHOP_SKIP_AGY_VERIFY")) {
		return
	}
	artVerifyOnce.Do(func() { go a.VerifyArtModels(ctx, agyMu) })
}

// ReverifyArtModels re-runs the agy art-model probe on demand (the settings
// panel's re-verify button — e.g. right after the operator logged agy in).
// Returns false when a verification is already in flight (the new request is
// dropped, not queued). Runs detached from the caller's request context: the
// probe must outlive the HTTP request that kicked it.
func (a *App) ReverifyArtModels() bool {
	if os.Getenv("WORKSHOP_FAKE_BIN") != "" || truthy(os.Getenv("WORKSHOP_SKIP_AGY_VERIFY")) {
		return false
	}
	if a.artVerifying.Load() {
		return false
	}
	go a.VerifyArtModels(context.Background(), &a.agyMu)
	return true
}

// VerifyArtModels probes agy for its model list (quota-free — see
// driver.(*Agy).ListModels) and records which allowed image model art passes'
// claude orchestrator should hand agy: the first domain.ArtAgyModels entry
// agy offers. Findings land
// in the kv store and on the event bus; per the project's warn-not-block
// model policy this never fails engine start — with no verified label, art
// passes assume the preferred default and fail visibly if agy rejects it.
func (a *App) VerifyArtModels(ctx context.Context, agyMu *sync.RWMutex) {
	if !a.artVerifying.CompareAndSwap(false, true) {
		return // a probe is already in flight; its result is as good as ours
	}
	defer a.artVerifying.Store(false)
	// The probe is itself an `agy -p` run, and agy rewrites its shared
	// last_conversations.json whole on every run — the record an in-flight
	// art pass depends on to resume its conversation. Take the ordinary-run
	// (read) slot of the engine's agy lock so the probe never overlaps an
	// art pass, same discipline as every agy spawn the engine launches.
	// (Cross-process runs — `workshop doctor`, the user's own agy — are
	// outside any lock; this covers what this process launches.)
	if agyMu != nil {
		agyMu.RLock()
		defer agyMu.RUnlock()
	}
	agy := driver.NewAgy()
	models, err := agy.ListModels(ctx)
	if err != nil {
		a.Bus.Publish(ctx, domain.Event{Type: "art.models_unverified", Payload: map[string]any{
			"error": err.Error(),
			"note":  "art passes will assume " + domain.ArtAgyModels[0],
		}})
		return
	}
	raw, _ := json.Marshal(models)
	_ = a.Store.SetKV(ctx, engine.KVArtAgyModels, string(raw))
	for _, want := range domain.ArtAgyModels {
		if driver.AgyHasModel(models, want) {
			_ = a.Store.SetKV(ctx, engine.KVArtAgyModel, want)
			a.Bus.Publish(ctx, domain.Event{Type: "art.model_verified", Payload: map[string]any{
				"model": want, "available": models,
			}})
			return
		}
	}
	// None of the allowed labels exist: clear any stale pick so passes
	// don't silently run a label a previous verification blessed.
	_ = a.Store.SetKV(ctx, engine.KVArtAgyModel, "")
	a.Bus.Publish(ctx, domain.Event{Type: "art.models_missing", Payload: map[string]any{
		"wanted":    domain.ArtAgyModels,
		"available": models,
		"note":      "agy offers none of the allowed art models — art passes will fail until agy is updated (or its login refreshed)",
	}})
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
