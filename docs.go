package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const modelsBegin = "<!-- orch:models:begin -->"
const modelsEnd = "<!-- orch:models:end -->"

var generatedModelDocs = []string{"README.md", "docs/setup.md", "skill/references/design.md", "constitution/orch-block.md"}

func tierSummary() string {
	var tiers []string
	for _, m := range catalog {
		if m.Family == familyOpenAI && (len(tiers) == 0 || tiers[len(tiers)-1] != m.Tier) {
			tiers = append(tiers, m.Tier)
		}
	}
	var aliases []string
	for _, target := range catalog {
		var names []string
		for alias, key := range embeddedManifest.Aliases {
			if key == target.Key {
				names = append(names, alias)
			}
		}
		slices.Sort(names)
		for _, alias := range names {
			aliases = append(aliases, alias+" -> "+target.Key)
		}
	}
	text := "OpenAI tiers: " + strings.Join(tiers, " < ") + " (independent of effort)."
	if len(aliases) > 0 {
		text += " Deprecated alias: " + strings.Join(aliases, ", ") + "."
	}
	for _, m := range catalog {
		if legacy := embeddedManifest.Rules.LegacyModelEnv[m.Key]; legacy != "" {
			text += " " + modelVar(m) + " takes precedence over " + legacy + "."
		}
	}
	return text
}
func modelMarkdown() string {
	var b strings.Builder
	b.WriteString("| Key | CLI | Family | Tier | Default model | Effort | Model variable |\n|---|---|---|---|---|---|---|\n")
	for _, m := range catalog {
		effort := m.Effort
		if effort == "" {
			effort = "—"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | `%s` | %s | `%s` |\n", m.Key, m.Kind, m.Family, m.Tier, m.Default, effort, modelVar(m))
	}
	fmt.Fprintf(&b, "\n%s\n\nDefaults are generated from `models.yaml`. Override shared model ids in `~/.orch/models.yaml` (or `ORCH_MODELS_FILE`), for example `models: {sol: new-model-id}` updates both Sol keys. An overlay may also set per-key effort with `keys: [{key: codex-high-l, effort: low}]`; other fields are rejected. Per-key environment variables override overlay model ids. `orch models` shows resolved ids and their source.\n", tierSummary())
	return b.String()
}

// generateModelDocs validates every marker pair before replacing only marked text.
func generateModelDocs(root string) error {
	type update struct {
		path string
		data []byte
		mode os.FileMode
	}
	var updates []update
	for _, file := range generatedModelDocs {
		path := filepath.Join(root, file)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		if strings.Count(text, modelsBegin) != 1 || strings.Count(text, modelsEnd) != 1 {
			return fmt.Errorf("%s: expected exactly one model marker pair", file)
		}
		from := strings.Index(text, modelsBegin) + len(modelsBegin)
		to := strings.Index(text, modelsEnd)
		if to < from {
			return fmt.Errorf("%s: reversed model markers", file)
		}
		block := "Catalog: [README](../README.md#model-catalog); defaults come from `models.yaml`; inspect resolved values with `orch models`.\n"
		if file == "skill/references/design.md" {
			block = "Catalog: [README](../../README.md#model-catalog); defaults come from `models.yaml`; inspect resolved values with `orch models`.\n"
		}
		if file == "README.md" {
			block = modelMarkdown()
		}
		if file == "constitution/orch-block.md" {
			block = "- " + tierSummary() + "\n"
		}
		next := text[:from] + "\n" + block + text[to:]
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		updates = append(updates, update{path, []byte(next), st.Mode()})
	}
	for _, u := range updates {
		if err := os.WriteFile(u.path, u.data, u.mode); err != nil {
			return err
		}
	}
	return nil
}
