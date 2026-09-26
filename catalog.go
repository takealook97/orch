package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// Model is one fixed model × effort entry. Heads choose by Key only; orch assembles the CLI arguments.
// Keys and efforts are fixed; the underlying model id comes from an environment variable so a CLI
// release can be picked up without a rebuild.
type Model struct {
	Key      string
	Kind     string // herdr agent kind
	Family   string
	Cost     string
	Summary  string
	ModelVar string // environment variable that overrides Default
	Default  string // model id used when ModelVar is unset
	Effort   string // empty when the CLI takes no effort flag
}

// Model id variables, one per underlying model. Keys sharing a model share its variable.
const (
	varLuna   = "ORCH_LUNA_MODEL"
	varSol    = "ORCH_SOL_MODEL"
	varOpus   = "ORCH_OPUS_MODEL"
	varSonnet = "ORCH_SONNET_MODEL"
	varFlash  = "ORCH_FLASH_MODEL"
)

const (
	familyOpenAI    = "openai"
	familyAnthropic = "anthropic"
	familyGoogle    = "google"
)

var catalog = []Model{
	{"luna-m", "codex", familyOpenAI, "lowest", "fast exploration, bulk classification, simple extraction", varLuna, "gpt-6-luna", "medium"},
	{"luna-h", "codex", familyOpenAI, "low", "precise extraction, log and test analysis, focused verification", varLuna, "gpt-6-luna", "high"},
	{"sol-l", "codex", familyOpenAI, "medium", "mechanical implementation from complete specs, bulk repetitive edits", varSol, "gpt-6-sol", "low"},
	{"sol-m", "codex", familyOpenAI, "medium+", "general implementation, debugging, integration, technical review", varSol, "gpt-6-sol", "medium"},
	{"opus-m", "claude", familyAnthropic, "highest", "hard implementation and debugging, ambiguous design, deep review, adversarial verification", varOpus, "opus", "medium"},
	{"sonnet-m", "claude", familyAnthropic, "medium", "docs and writing, alternative implementation, code review, scenario QA", varSonnet, "sonnet", "medium"},
	{"flash", "agy", familyGoogle, "low", "neutral verification, web and document research, multimodal inspection, rendering checks", varFlash, "gemini-3.8-flash-medium", ""},
}

// modelID returns the model id for m, preferring its environment variable over the default.
func modelID(m Model) (string, error) {
	id := strings.TrimSpace(os.Getenv(m.ModelVar))
	if id == "" {
		return m.Default, nil
	}
	if strings.HasPrefix(id, "-") || strings.ContainsAny(id, " \t\n\"'") {
		return "", fmt.Errorf("%s=%q is not a valid model id", m.ModelVar, id)
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
	"explore":     {"luna-m", "luna-h", "flash"},
	"extract":     {"luna-h", "sol-m", "sonnet-m"},
	"web":         {"flash", "sonnet-m", "opus-m"},
	"synthesis":   {"opus-m", "sol-m", "sonnet-m"},
	"mech-impl":   {"sol-l", "sol-m", "sonnet-m"},
	"impl":        {"sol-m", "opus-m", "sonnet-m"},
	"hard-impl":   {"opus-m", "sol-m", "sonnet-m"},
	"alt":         {"sonnet-m", "sol-m", "opus-m"},
	"docs":        {"sonnet-m", "opus-m", "sol-m"},
	"analysis":    {"sol-m", "opus-m", "sonnet-m"},
	"integrate":   {"sol-m", "opus-m"},
	"image":       {"sol-m"},
	"verify-mech": {"luna-h", "sol-m", "sonnet-m"},
	// Verification chains are filtered by family constraints at spawn time.
	"verify-critical":    {"sonnet-m", "opus-m", "sol-m", "luna-h"},
	"verify-neutral":     {"flash", "sonnet-m", "sol-m"},
	"verify-adversarial": {"opus-m", "sol-m", "sonnet-m"},
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
		if m.Key != "flash" {
			warnings = append(warnings, fmt.Sprintf("%s takes the neutral lane instead of flash. Mark it as 'flash substitute' in the final report", m.Key))
		}
	case roleAdversarial:
		if m.Key == "flash" {
			return nil, fmt.Errorf("do not use flash for an adversarial lane")
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
