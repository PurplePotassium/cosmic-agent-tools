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
// green/blue-screen remover art-gen-trans uses (live-switchable) and what the
// agy model verification found.
type ArtStatus struct {
	Remover    string   `json:"remover"`            // effective remover
	Removers   []string `json:"removers"`           // selectable values
	Override   string   `json:"override,omitempty"` // live override ("" = using config)
	Configured string   `json:"configured"`         // [art].remover config value
	// Model is the launch-verified agy label art passes run ("" = probe has
	// not succeeded; passes then assume the preferred default).
	Model string `json:"model,omitempty"`
	// AgyModels is every label the last successful probe saw.
	AgyModels []string `json:"agyModels,omitempty"`
	// Wanted is the ordered preference list of allowed art models.
	Wanted []string `json:"wanted"`
}

// ArtStatus assembles the current art settings view.
func (a *App) ArtStatus(ctx context.Context) ArtStatus {
	st := ArtStatus{
		Removers:   chroma.Removers,
		Configured: a.Res().Config.Art.Remover,
		Wanted:     domain.ArtAgyModels,
	}
	if st.Configured == "" {
		st.Configured = chroma.Removers[0]
	}
	st.Remover = st.Configured
	if v, err := a.Store.GetKV(ctx, engine.KVArtRemover); err == nil && v != "" {
		st.Override = v
		st.Remover = v
	}
	st.Model, _ = a.Store.GetKV(ctx, engine.KVArtAgyModel)
	if raw, err := a.Store.GetKV(ctx, engine.KVArtAgyModels); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &st.AgyModels)
	}
	return st
}

// SetArtRemover sets (or, for "", clears) the live green/blue-screen remover
// override. Art passes re-read it every pass, so it takes effect on the NEXT
// art-gen-trans pass without a restart — the art counterpart of the pipeline
// bundle/mode/personality overrides.
func (a *App) SetArtRemover(ctx context.Context, remover string) error {
	if !chroma.ValidRemover(remover) {
		return fmt.Errorf("remover %q is not one of %v", remover, chroma.Removers)
	}
	if err := a.Store.SetKV(ctx, engine.KVArtRemover, remover); err != nil {
		return err
	}
	a.Bus.Publish(ctx, domain.Event{Type: "art.remover", Payload: map[string]any{
		"remover": remover, "cleared": remover == "",
	}})
	return nil
}

// artVerifyOnce dedupes the launch probe across engine relaunches (dashboard
// halt/resume restarts RunHeadless within the same process).
var artVerifyOnce sync.Once

// VerifyArtModelsAsync launches the agy art-model verification in the
// background — once per process, and never in test harnesses (the fake-agent
// env) where spawning a real agy would hit the network and pollute agy's
// history.
func (a *App) VerifyArtModelsAsync(ctx context.Context) {
	if os.Getenv("WORKSHOP_FAKE_BIN") != "" || truthy(os.Getenv("WORKSHOP_SKIP_AGY_VERIFY")) {
		return
	}
	artVerifyOnce.Do(func() { go a.VerifyArtModels(ctx) })
}

// VerifyArtModels probes agy for its model list (quota-free — see
// driver.(*Agy).ListModels) and records which allowed art model art passes
// should run: the first domain.ArtAgyModels entry agy offers. Findings land
// in the kv store and on the event bus; per the project's warn-not-block
// model policy this never fails engine start — with no verified label, art
// passes assume the preferred default and fail visibly if agy rejects it.
func (a *App) VerifyArtModels(ctx context.Context) {
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
