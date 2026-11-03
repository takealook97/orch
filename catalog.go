package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Model is one fixed model × effort entry. Heads choose by Key only; orch assembles the CLI arguments.
// Keys are <CLI>-<tier>-<effort initial>, where the tier is the model's rank in its vendor lineup,
// so a key survives a model rename. The embedded manifest supplies defaults; a local overlay
// can update shared model ids and per-key effort, and per-key env vars override the model id.
type Model struct {
	Name    string
	Tier    string
	Key     string
	Kind    string // herdr agent kind
	Family  string
	Cost    string
	Summary string
	Default string // model id used when modelVar(m) is unset
	Effort  string // empty when the CLI takes no effort flag (gemini carries it in the model id)
	// An invocation pins its selection before slow account or pane setup begins.
	resolvedID     string
	resolvedSource string
}

const (
	familyOpenAI    = "openai"
	familyAnthropic = "anthropic"
	familyGoogle    = "google"
)

var embeddedManifest = mustManifest()
var catalog, _ = embeddedManifest.catalog()

// modelVar is the environment variable that overrides m's model id: the key upper-cased with
// underscores (codex-high-m -> CODEX_HIGH_M).
func modelVar(m Model) string { return strings.ToUpper(strings.ReplaceAll(m.Key, "-", "_")) }

// modelID returns the model id for m, preferring its environment variable over the default.
func modelID(m Model) (string, error) { id, _, err := resolvedModel(m); return id, err }

// canonicalModelKey preserves saved runs and requests using the former Luna key.
func canonicalModelKey(key string) string {
	if k, ok := embeddedManifest.Aliases[key]; ok {
		return k
	}
	return key
}

func modelByKey(key string) (Model, error) {
	key = canonicalModelKey(key)
	for _, m := range catalog {
		if m.Key == key {
			resolved, _, _, err := resolveModel(m)
			return resolved, err
		}
	}
	keys := make([]string, 0, len(catalog))
	for _, m := range catalog {
		keys = append(keys, m.Key)
	}
	return Model{}, fmt.Errorf("model key not in catalog %q (allowed: %s)", key, strings.Join(keys, ", "))
}

// launchArgs returns the native CLI arguments for a worker, including the bypass flag.
func launchArgs(m Model, noDaemon bool) ([]string, error) {
	a, err := adapterFor(m.Kind)
	if err != nil {
		return nil, err
	}
	return a.interactive(m, noDaemon)
}

// Roles (contract stages). Verification roles carry family constraints.
const (
	roleRecon       = "recon"
	roleDesign      = "design"
	roleProduce     = "produce"
	roleIntegrate   = "integrate"
	roleMech        = "verify-mech"
	roleCritical    = "verify-critical"
	roleNeutral     = "verify-neutral"
	roleAdversarial = "verify-adversarial"
)

var roles = []string{roleRecon, roleDesign, roleProduce, roleIntegrate, roleMech, roleCritical, roleNeutral, roleAdversarial}

func isVerifyRole(role string) bool { return strings.HasPrefix(role, "verify-") }

// Fallback chains by purpose (design §9.4). The first entry is the primary key.
var chains = embeddedManifest.Fallbacks

func purposeNames() []string {
	out := make([]string, 0, len(chains))
	for k := range chains {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// checkLane enforces design §9.3. authors are the families of the reviewed workers;
// critics are the families of existing critical reviewers of the same targets.
// It returns an error for a hard violation and warnings for soft ones.
func checkLane(role string, m Model, authors, critics []string) (warnings []string, err error) {
	if !isVerifyRole(role) {
		return nil, nil
	}
	if role != roleMech && len(authors) == 0 {
		return nil, fmt.Errorf("%s lane requires --reviews (worker under review) or --author-family (author outside the run, such as Head)", role)
	}
	switch role {
	case roleCritical:
		if slices.Contains(authors, m.Family) {
			return nil, fmt.Errorf("critical lane must use a different family from the author: %s(%s) matches the author family", m.Key, m.Family)
		}
	case roleNeutral:
		if slices.Contains(authors, m.Family) {
			return nil, fmt.Errorf("neutral lane must use a different family from the author: %s(%s)", m.Key, m.Family)
		}
		if m.Key != embeddedManifest.Rules.NeutralKey {
			warnings = append(warnings, fmt.Sprintf("%s takes the neutral lane instead of %s. Mark it as '%s substitute' in the final report", m.Key, embeddedManifest.Rules.NeutralKey, embeddedManifest.Rules.NeutralKey))
		}
	case roleAdversarial:
		if slices.Contains(embeddedManifest.Rules.AdversarialExclude, m.Key) {
			return nil, fmt.Errorf("do not use %s for an adversarial lane", m.Key)
		}
		if slices.Contains(authors, m.Family) {
			return nil, fmt.Errorf("adversarial lane must use a different family from the author: %s(%s)", m.Key, m.Family)
		}
		if slices.Contains(critics, m.Family) {
			warnings = append(warnings, "adversarial lane shares the critical reviewer family (recommended separation waived; sessions remain separate)")
		}
	}
	return warnings, nil
}

// nextFallback returns the next key in the purpose chain after current that passes lane checks.
func nextFallback(purpose, current, role string, authors, critics []string, tried []string) (Model, bool) {
	chain := chains[purpose]
	start := slices.Index(chain, canonicalModelKey(current)) + 1
	// Normalize into fresh storage: callers may retain saved legacy keys.
	triedCanonical := make([]string, 0, len(tried))
	for _, key := range tried {
		triedCanonical = append(triedCanonical, canonicalModelKey(key))
	}
	for _, key := range chain[start:] {
		if slices.Contains(triedCanonical, key) || slices.Contains(embeddedManifest.Rules.FallbackSkip[canonicalModelKey(current)], key) {
			continue
		}
		m, err := modelByKey(key)
		if err != nil {
			continue
		}
		if _, err := checkLane(role, m, authors, critics); err != nil {
			continue
		}
		return m, true
	}
	return Model{}, false
}

// withoutKind adds every chain key of the given CLI kind to tried, so a fallback must
// change CLI when all accounts of one kind are spent.
func withoutKind(purpose, kind string, tried []string) []string {
	out := slices.Clone(tried)
	for _, key := range chains[purpose] {
		if m, err := modelByKey(key); err == nil && m.Kind == kind {
			out = append(out, key)
		}
	}
	return out
}

//go:embed models.yaml
var embeddedModels []byte

type manifestKey struct {
	Key    string `yaml:"key"`
	Kind   string `yaml:"kind"`
	Family string `yaml:"family"`
	Tier   string `yaml:"tier"`
	Model  string `yaml:"model"`
	Effort string `yaml:"effort"`
	Cost   string `yaml:"cost"`
	Use    string `yaml:"use"`
}
type manifestRules struct {
	LegacyModelEnv     map[string]string   `yaml:"legacy_model_env"`
	FallbackSkip       map[string][]string `yaml:"fallback_skip"`
	NeutralKey         string              `yaml:"neutral_key"`
	AdversarialExclude []string            `yaml:"adversarial_exclude"`
}
type modelManifest struct {
	Version   int                 `yaml:"version"`
	Models    map[string]string   `yaml:"models"`
	Keys      []manifestKey       `yaml:"keys"`
	Aliases   map[string]string   `yaml:"aliases"`
	Fallbacks map[string][]string `yaml:"fallbacks"`
	Rules     manifestRules       `yaml:"rules"`
}

func decodeModels(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("models manifest must contain one YAML document")
	}
	return nil
}
func validModelID(id string) bool {
	return id != "" && id == strings.TrimSpace(id) && !strings.HasPrefix(id, "-") && !strings.ContainsAny(id, " \t\r\n\"'")
}
func validEffort(e string) bool {
	return slices.Contains([]string{"", "low", "medium", "high", "xhigh"}, e)
}
func loadManifest(data []byte) (*modelManifest, error) {
	var mf modelManifest
	if err := decodeModels(data, &mf); err != nil {
		return nil, fmt.Errorf("model manifest: %w", err)
	}
	if mf.Version != 1 {
		return nil, fmt.Errorf("unsupported model manifest version %d", mf.Version)
	}
	if len(mf.Models) == 0 || len(mf.Keys) == 0 {
		return nil, fmt.Errorf("model manifest needs models and keys")
	}
	for name, id := range mf.Models {
		if !validModelID(id) {
			return nil, fmt.Errorf("invalid model id for %s: %q", name, id)
		}
	}
	keys := map[string]bool{}
	for _, k := range mf.Keys {
		if k.Key == "" || keys[k.Key] {
			return nil, fmt.Errorf("empty or duplicate model key %q", k.Key)
		}
		keys[k.Key] = true
		if _, ok := mf.Models[k.Model]; !ok {
			return nil, fmt.Errorf("%s: unknown model name %q", k.Key, k.Model)
		}
		if _, err := adapterFor(k.Kind); err != nil {
			return nil, err
		}
		if k.Family != map[string]string{"codex": familyOpenAI, "claude": familyAnthropic, "agy": familyGoogle}[k.Kind] || k.Tier == "" || !validEffort(k.Effort) {
			return nil, fmt.Errorf("invalid family, tier or effort for %s", k.Key)
		}
		if _, suffix := claudeModelArgs(mf.Models[k.Model]); k.Kind == "claude" && suffix != "" && k.Effort != "" {
			return nil, fmt.Errorf("%s: effort %q conflicts with effort suffix %q in model name", k.Key, k.Effort, suffix)
		}
	}
	for alias, target := range mf.Aliases {
		if alias == "" || keys[alias] || !keys[target] {
			return nil, fmt.Errorf("invalid alias %s -> %s", alias, target)
		}
	}
	checkTargets := func(label string, targets []string) error {
		for _, k := range targets {
			if !keys[k] {
				return fmt.Errorf("%s: unknown target %s", label, k)
			}
		}
		return nil
	}
	for name, chain := range mf.Fallbacks {
		if len(chain) == 0 {
			return nil, fmt.Errorf("empty fallback chain %s", name)
		}
		if err := checkTargets(name, chain); err != nil {
			return nil, err
		}
	}
	for key, targets := range mf.Rules.FallbackSkip {
		if !keys[key] {
			return nil, fmt.Errorf("unknown fallback rule key %s", key)
		}
		if err := checkTargets(key, targets); err != nil {
			return nil, err
		}
	}
	for key, env := range mf.Rules.LegacyModelEnv {
		if !keys[key] || env == "" {
			return nil, fmt.Errorf("invalid legacy env rule %s", key)
		}
	}
	if err := checkTargets("neutral", []string{mf.Rules.NeutralKey}); err != nil {
		return nil, err
	}
	if err := checkTargets("adversarial", mf.Rules.AdversarialExclude); err != nil {
		return nil, err
	}
	return &mf, nil
}
func mustManifest() *modelManifest {
	mf, err := loadManifest(embeddedModels)
	if err != nil {
		panic(err)
	}
	return mf
}
func (mf *modelManifest) catalog() ([]Model, error) {
	out := make([]Model, 0, len(mf.Keys))
	for _, k := range mf.Keys {
		id, ok := mf.Models[k.Model]
		if !ok {
			return nil, fmt.Errorf("unknown model name %s", k.Model)
		}
		out = append(out, Model{Key: k.Key, Kind: k.Kind, Family: k.Family, Tier: k.Tier, Name: k.Model, Cost: k.Cost, Summary: k.Use, Default: id, Effort: k.Effort})
	}
	return out, nil
}

type modelOverlay struct {
	Version *int              `yaml:"version"`
	Models  map[string]string `yaml:"models"`
	Keys    []struct {
		Key    string  `yaml:"key"`
		Effort *string `yaml:"effort"`
	} `yaml:"keys"`
}

func readModelOverlay() (*modelOverlay, error) {
	path := os.Getenv("ORCH_MODELS_FILE")
	if path == "" {
		path = filepath.Join(orchHome(), "models.yaml")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &modelOverlay{}, nil
	}
	if err != nil {
		return nil, err
	}
	var ov modelOverlay
	if err := decodeModels(data, &ov); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if ov.Version != nil && *ov.Version != 1 {
		return nil, fmt.Errorf("%s: unsupported overlay version %d", path, *ov.Version)
	}
	for name, id := range ov.Models {
		if _, ok := embeddedManifest.Models[name]; !ok {
			return nil, fmt.Errorf("%s: unknown model name %s", path, name)
		}
		if !validModelID(id) {
			return nil, fmt.Errorf("%s: invalid model id %q", path, id)
		}
	}
	seen := map[string]bool{}
	for _, k := range ov.Keys {
		found := false
		embeddedEffort := ""
		for _, m := range catalog {
			if m.Key == k.Key {
				found = true
				embeddedEffort = m.Effort
				break
			}
		}
		if !found || seen[k.Key] || k.Effort == nil || !validEffort(*k.Effort) || (*k.Effort == "" && embeddedEffort != "") {
			return nil, fmt.Errorf("%s: invalid or duplicate effort override for %q", path, k.Key)
		}
		seen[k.Key] = true
	}
	return &ov, nil
}

// resolveModel applies the shared model-name overlay, then the existing per-key env overrides.
// It leaves the embedded catalog immutable, so reads never leak settings into later commands.
func resolveModel(m Model) (Model, string, string, error) {
	if m.resolvedID != "" {
		return m, m.resolvedID, m.resolvedSource, nil
	}
	// Start from immutable defaults even if the caller retained a previously resolved entry.
	for _, base := range catalog {
		if base.Key == m.Key {
			m = base
			break
		}
	}
	ov, err := readModelOverlay()
	if err != nil {
		return m, "", "", err
	}
	source := "embedded"
	if id, ok := ov.Models[m.Name]; ok {
		m.Default = id
		source = "overlay"
	}
	for _, k := range ov.Keys {
		if k.Key == m.Key {
			m.Effort = *k.Effort
		}
	}
	env := modelVar(m)
	id := strings.TrimSpace(os.Getenv(env))
	if legacy := embeddedManifest.Rules.LegacyModelEnv[m.Key]; id == "" && legacy != "" {
		env = legacy
		id = strings.TrimSpace(os.Getenv(env))
	}
	if id != "" {
		if !validModelID(id) {
			return m, "", "", fmt.Errorf("%s=%q is not a valid model id", env, id)
		}
		return m, id, "env", nil
	}
	return m, m.Default, source, nil
}
func resolvedModel(m Model) (string, string, error) {
	_, id, source, err := resolveModel(m)
	return id, source, err
}

func freezeModel(m Model) (Model, string, string, error) {
	m, id, source, err := resolveModel(m)
	if err == nil {
		m.resolvedID, m.resolvedSource = id, source
	}
	return m, id, source, err
}
