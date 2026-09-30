package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// Model is one fixed model × effort entry. Heads choose by Key only; orch assembles the CLI arguments.
// Keys are <CLI>-<tier>-<effort initial>, where the tier is the model's rank in its vendor lineup,
// so a key survives a model rename. Keys and efforts are fixed; each key's model id comes from its
// own environment variable (see modelVar) so a CLI release can be picked up without a rebuild.
type Model struct {
	Key     string
	Kind    string // herdr agent kind
	Family  string
	Cost    string
	Summary string
	Default string // model id used when modelVar(m) is unset
	Effort  string // empty when the CLI takes no effort flag (gemini carries it in the model id)
}

const (
	familyOpenAI    = "openai"
	familyAnthropic = "anthropic"
	familyGoogle    = "google"
)

var catalog = []Model{
	{"codex-medium-h", "codex", familyOpenAI, "low", "exploration, bulk classification, precise extraction, log and test analysis, focused verification", "gpt-6-luna", "high"},
	{"codex-high-l", "codex", familyOpenAI, "medium", "mechanical implementation from complete specs, bulk repetitive edits", "gpt-6.1-sol", "low"},
	{"codex-high-m", "codex", familyOpenAI, "medium+", "general implementation, debugging, integration, technical review", "gpt-6.1-sol", "medium"},
	{"claude-high-m", "claude", familyAnthropic, "highest", "hard implementation and debugging, ambiguous design, deep review, adversarial verification", "opus", "medium"},
	{"claude-medium-m", "claude", familyAnthropic, "medium", "docs and writing, alternative implementation, code review, scenario QA", "sonnet", "medium"},
	{"gemini-medium-m", "agy", familyGoogle, "low", "neutral verification, web and document research, multimodal inspection, rendering checks", "gemini-3.8-flash-medium", ""},
}

// modelVar is the environment variable that overrides m's model id: the key upper-cased with
// underscores (codex-high-m -> CODEX_HIGH_M).
func modelVar(m Model) string { return strings.ToUpper(strings.ReplaceAll(m.Key, "-", "_")) }

// modelID returns the model id for m, preferring its environment variable over the default.
func modelID(m Model) (string, error) {
	id := strings.TrimSpace(os.Getenv(modelVar(m)))
	if id == "" {
		return m.Default, nil
	}
	if strings.HasPrefix(id, "-") || strings.ContainsAny(id, " \t\n\"'") {
		return "", fmt.Errorf("%s=%q is not a valid model id", modelVar(m), id)
	}
	return id, nil
}

// modelArgs returns the CLI arguments that select m's model and effort.
func modelArgs(m Model) ([]string, error) {
	id, err := modelID(m)
	if err != nil {
		return nil, err
	}
	args := []string{"--model", id}
	switch {
	case m.Effort == "":
	case m.Kind == "codex":
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", m.Effort))
	default:
		args = append(args, "--effort", m.Effort)
	}
	return args, nil
}

func modelByKey(key string) (Model, error) {
	for _, m := range catalog {
		if m.Key == key {
			return m, nil
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
	margs, err := modelArgs(m)
	if err != nil {
		return nil, err
	}
	var args []string
	switch m.Kind {
	case "codex":
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		if noDaemon {
			args = append(args, "--no-daemon")
		}
	case "claude", "agy":
		args = append(args, "--dangerously-skip-permissions")
	}
	return append(args, margs...), nil
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
var chains = map[string][]string{
	"explore":     {"codex-medium-h", "gemini-medium-m"},
	"extract":     {"codex-medium-h", "codex-high-m", "claude-medium-m"},
	"web":         {"gemini-medium-m", "claude-medium-m", "claude-high-m"},
	"synthesis":   {"claude-high-m", "codex-high-m", "claude-medium-m"},
	"mech-impl":   {"codex-high-l", "codex-high-m", "claude-medium-m"},
	"impl":        {"codex-high-m", "claude-high-m", "claude-medium-m"},
	"hard-impl":   {"claude-high-m", "codex-high-m", "claude-medium-m"},
	"alt":         {"claude-medium-m", "codex-high-m", "claude-high-m"},
	"docs":        {"claude-medium-m", "claude-high-m", "codex-high-m"},
	"analysis":    {"codex-high-m", "claude-high-m", "claude-medium-m"},
	"integrate":   {"codex-high-m", "claude-high-m"},
	"image":       {"codex-high-m"},
	"verify-mech": {"codex-medium-h", "codex-high-m", "claude-medium-m"},
	// Verification chains are filtered by family constraints at spawn time.
	"verify-critical":    {"claude-medium-m", "claude-high-m", "codex-high-l", "codex-high-m"},
	"verify-neutral":     {"gemini-medium-m", "claude-medium-m", "codex-high-m"},
	"verify-adversarial": {"claude-high-m", "codex-high-m", "claude-medium-m"},
}

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
		if m.Key != "gemini-medium-m" {
			warnings = append(warnings, fmt.Sprintf("%s takes the neutral lane instead of gemini-medium-m. Mark it as 'gemini-medium-m substitute' in the final report", m.Key))
		}
	case roleAdversarial:
		if m.Key == "gemini-medium-m" {
			return nil, fmt.Errorf("do not use gemini-medium-m for an adversarial lane")
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
	start := slices.Index(chain, current) + 1
	for _, key := range chain[start:] {
		if slices.Contains(tried, key) {
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
