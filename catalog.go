package main

import (
	"fmt"
	"slices"
	"strings"
)

// Model is one fixed model × effort entry. Heads choose by Key only; orch assembles the CLI arguments.
type Model struct {
	Key     string
	Kind    string // herdr agent kind
	Family  string
	Cost    string
	Summary string
	Args    []string
}

const (
	familyOpenAI    = "openai"
	familyAnthropic = "anthropic"
	familyGoogle    = "google"
)

var catalog = []Model{
	{"luna-m", "codex", familyOpenAI, "lowest", "fast exploration, bulk classification, simple extraction",
		[]string{"--model", "gpt-6-luna", "-c", `model_reasoning_effort="medium"`}},
	{"luna-h", "codex", familyOpenAI, "low", "precise extraction, log and test analysis, focused verification",
		[]string{"--model", "gpt-6-luna", "-c", `model_reasoning_effort="high"`}},
	{"sol-l", "codex", familyOpenAI, "medium", "mechanical implementation from complete specs, bulk repetitive edits",
		[]string{"--model", "gpt-6-sol", "-c", `model_reasoning_effort="low"`}},
	{"sol-m", "codex", familyOpenAI, "medium+", "general implementation, debugging, integration, technical review",
		[]string{"--model", "gpt-6-sol", "-c", `model_reasoning_effort="medium"`}},
	{"opus-m", "claude", familyAnthropic, "highest", "hard implementation and debugging, ambiguous design, deep review, adversarial verification",
		[]string{"--model", "opus", "--effort", "medium"}},
	{"sonnet-m", "claude", familyAnthropic, "medium", "docs and writing, alternative implementation, code review, scenario QA",
		[]string{"--model", "sonnet", "--effort", "medium"}},
	{"flash", "agy", familyGoogle, "low", "neutral verification, web and document research, multimodal inspection, rendering checks",
		[]string{"--model", "gemini-3.8-flash-medium"}},
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
func launchArgs(m Model, noDaemon bool) []string {
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
	return append(args, m.Args...)
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
