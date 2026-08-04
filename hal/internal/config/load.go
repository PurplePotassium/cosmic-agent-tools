package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// Layer names used in provenance, lowest to highest precedence.
const (
	LayerBuiltin  = "builtin"
	LayerUser     = "user"
	LayerRepo     = "repo"
	LayerOverride = "override"
	LayerEnv      = "env"
	LayerFlag     = "flag"
)

// Result is a resolved configuration plus where every key came from.
type Result struct {
	Config     Config
	Provenance map[string]string // dotted key -> layer name; absent = builtin
	Warnings   []string
}

// Note records provenance for a key set outside Load (e.g. CLI flags).
func (r *Result) Note(dottedKey, layer string) {
	if r.Provenance == nil {
		r.Provenance = map[string]string{}
	}
	r.Provenance[dottedKey] = layer
}

// Source resolves the provenance for a dotted key.
func (r *Result) Source(dottedKey string) string {
	if l, ok := r.Provenance[dottedKey]; ok {
		return l
	}
	return LayerBuiltin
}

// Load resolves configuration from the file layers plus environment.
// Missing files are skipped. env is usually os.Getenv (injectable for tests).
func Load(userFile, repoFile, overridesFile string, env func(string) string) (*Result, error) {
	res := &Result{Config: Default(), Provenance: map[string]string{}}

	merged := map[string]any{}
	type layerFile struct{ name, path string }
	for _, lf := range []layerFile{
		{LayerUser, userFile},
		{LayerRepo, repoFile},
		{LayerOverride, overridesFile},
	} {
		if lf.path == "" {
			continue
		}
		raw, err := os.ReadFile(lf.path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("config: read %s: %w", lf.path, err)
		}
		var doc map[string]any
		if err := toml.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("config: parse %s (%s layer): %w", lf.path, lf.name, err)
		}
		mergeMaps(merged, doc, "", lf.name, res.Provenance)
	}

	if len(merged) > 0 {
		buf, err := toml.Marshal(merged)
		if err != nil {
			return nil, fmt.Errorf("config: re-encode merged layers: %w", err)
		}
		dec := toml.NewDecoder(strings.NewReader(string(buf)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&res.Config); err != nil {
			var strict *toml.StrictMissingError
			if errors.As(err, &strict) {
				for _, e := range strict.Errors {
					res.Warnings = append(res.Warnings, fmt.Sprintf("unknown config key: %s", strings.Join(e.Key(), ".")))
				}
				// Re-decode leniently over fresh defaults so known keys apply.
				res.Config = Default()
				if err := toml.Unmarshal(buf, &res.Config); err != nil {
					return nil, fmt.Errorf("config: decode merged layers: %w", err)
				}
			} else {
				return nil, fmt.Errorf("config: decode merged layers: %w", err)
			}
		}
	}

	applyEnv(res, env)
	applyDeprecations(res)

	errs, warns := res.Config.Validate()
	if len(errs) > 0 {
		// Blocking: for an unattended tool, running with a malformed
		// pipeline or safety knob is worse than refusing to start.
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		sort.Strings(msgs)
		return nil, fmt.Errorf("config: %s", strings.Join(msgs, "; "))
	}
	for _, w := range warns {
		res.Warnings = append(res.Warnings, w.Error())
	}
	sort.Strings(res.Warnings)
	return res, nil
}

// mergeMaps deep-merges src over dst. Tables merge recursively; everything
// else — including arrays like [[pipelines]] — replaces wholesale, and the
// winning layer is recorded per dotted key.
func mergeMaps(dst, src map[string]any, prefix, layer string, prov map[string]string) {
	for k, v := range src {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sm, ok := v.(map[string]any); ok {
			if dm, ok2 := dst[k].(map[string]any); ok2 {
				mergeMaps(dm, sm, key, layer, prov)
				continue
			}
			cp := map[string]any{}
			mergeMaps(cp, sm, key, layer, prov)
			dst[k] = cp
			continue
		}
		dst[k] = v
		prov[key] = layer
	}
}

// applyDeprecations warns about configured pass-loop keys the interactive
// hal no longer reads. The keys parse tolerantly (an old config file
// must never fail startup) but are NEVER used; provenance says whether a
// file layer actually set one.
func applyDeprecations(res *Result) {
	set := func(key string) bool {
		for k := range res.Provenance {
			if k == key || strings.HasPrefix(k, key+".") {
				return true
			}
		}
		return false
	}
	dep := func(key, msg string) {
		if set(key) {
			res.Warnings = append(res.Warnings, msg)
		}
	}
	dep("pipelines", "config: [[pipelines]] is ignored — hal now runs interactive workflows (see [workflow])")
	dep("spice", "config: [spice] is ignored — the autonomous pass loop was removed")
	dep("personality", "config: [personality] is ignored — the autonomous pass loop was removed")
	dep("classifier", "config: [classifier] is ignored — task classification died with the pass loop")
	dep("git", "config: [git] worktrees/branch_prefix are ignored — workflows run on the main checkout")
	dep("safety.max_iterations", "config: safety.max_iterations is ignored — workflows are operator-gated, not iteration-bounded")
	dep("safety.breaker_failures", "config: safety.breaker_failures is ignored — the pass-loop breaker was removed")
	dep("safety.sleep_seconds", "config: safety.sleep_seconds is ignored — there is no pass loop to pace")
	dep("server.planning_percent", "config: server.planning_percent is ignored — idle planning passes were removed")
	dep("server.planning_proposal_max", "config: server.planning_proposal_max is ignored — idle planning passes were removed")
	dep("server.follow_up_proposal_max", "config: server.follow_up_proposal_max is ignored — agent proposals were removed")
	dep("server.start_stopped", "config: server.start_stopped is ignored — workflows are idle until driven from the dashboard")
}

// applyEnv applies HAL_* environment overrides (machine-level plumbing).
func applyEnv(res *Result, env func(string) string) {
	if env == nil {
		env = os.Getenv
	}
	if v := env("HAL_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			res.Config.Server.Port = p
			res.Note("server.port", LayerEnv)
		} else {
			res.Warnings = append(res.Warnings, fmt.Sprintf("HAL_PORT=%q is not a number (ignored)", v))
		}
	}
	if v := env("HAL_NO_OPEN"); isTruthy(v) {
		res.Config.Server.OpenBrowser = false
		res.Note("server.open_browser", LayerEnv)
	}
	if v := env("HAL_TRUNK"); v != "" {
		res.Config.Project.Trunk = v
		res.Note("project.trunk", LayerEnv)
	}
	if v := env("HAL_VERIFY"); v != "" {
		res.Config.Project.Verify = v
		res.Note("project.verify", LayerEnv)
	}
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
