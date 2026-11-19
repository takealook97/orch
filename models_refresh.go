package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// modelsRefreshOptions is shared with `orch update --models`; keep the shape stable.
type modelsRefreshOptions struct {
	Repo   string
	DryRun bool
	Out    io.Writer
}

// refreshDeps holds every side effect of a refresh so tests can stub them.
type refreshDeps struct {
	run   func(ctx context.Context, dir, name string, args ...string) (string, error)
	probe func(ctx context.Context, m Model, id string) error
	now   func() time.Time
}

var refreshHooks = refreshDeps{run: refreshRun, probe: refreshProbe, now: time.Now}

const (
	refreshTestPattern  = "Catalog|Models|Docs"
	refreshInstallShell = "make build link && ./bin/orch install --repo ."
	claudeSkipReason    = "claude CLI has no model list command (claude --help lists none)"
)

// modelIDRe splits ids like gpt-6.2-sol or gemini-3.8-flash-medium into prefix, version,
// family word and an optional suffix (often the effort) that must be preserved.
var modelIDRe = regexp.MustCompile(`^(gpt|gemini)-(\d+(?:\.\d+)*)-([a-z]+)(-[a-z]+)?$`)

type modelIDParts struct{ prefix, version, word, suffix string }

func parseModelID(id string) (modelIDParts, bool) {
	g := modelIDRe.FindStringSubmatch(id)
	if g == nil {
		return modelIDParts{}, false
	}
	return modelIDParts{g[1], g[2], g[3], g[4]}, true
}

// compareVersions compares dotted numeric versions; missing parts count as zero.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		x, y := 0, 0
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return map[bool]int{true: -1, false: 1}[x < y]
		}
	}
	return 0
}

// newestModelID returns the highest-versioned id in list that shares current's prefix,
// family word and suffix, or "" when nothing newer than current exists.
func newestModelID(current string, list []string) string {
	cur, ok := parseModelID(current)
	if !ok {
		return ""
	}
	best, bestVersion := "", cur.version
	for _, id := range list {
		p, ok := parseModelID(id)
		if !ok || p.prefix != cur.prefix || p.word != cur.word || p.suffix != cur.suffix {
			continue
		}
		if compareVersions(p.version, bestVersion) > 0 {
			best, bestVersion = id, p.version
		}
	}
	return best
}

// parseCodexModels reads `codex debug models` JSON and keeps listed slugs only.
func parseCodexModels(data []byte) ([]string, error) {
	var doc struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("codex debug models: %w", err)
	}
	var out []string
	for _, m := range doc.Models {
		if m.Visibility == "list" && validModelID(m.Slug) {
			out = append(out, m.Slug)
		}
	}
	return out, nil
}

// parseAgyModels reads `agy models` lines of the form "<id>\t<display name>".
func parseAgyModels(data []byte) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.Contains(line, "\t") && validModelID(fields[0]) {
			out = append(out, fields[0])
		}
	}
	return out
}

type modelSource struct {
	name  string
	ids   []string
	err   error
	ready bool
}

// sourceFor maps a current id to the CLI list that can update it.
func sourceFor(id string) string {
	p, ok := parseModelID(id)
	switch {
	case ok && p.prefix == "gpt":
		return "codex"
	case ok && p.prefix == "gemini":
		return "agy"
	case strings.HasPrefix(id, "opus-") || strings.HasPrefix(id, "sonnet-") || strings.HasPrefix(id, "haiku-"):
		return "claude"
	}
	return ""
}

func loadSource(ctx context.Context, name string) modelSource {
	src := modelSource{name: name, ready: true}
	switch name {
	case "codex":
		out, err := refreshHooks.run(ctx, "", "codex", "debug", "models")
		if err != nil {
			src.err = err
			return src
		}
		src.ids, src.err = parseCodexModels([]byte(out))
	case "agy":
		out, err := refreshHooks.run(ctx, "", "agy", "models")
		if err != nil {
			src.err = err
			return src
		}
		src.ids = parseAgyModels([]byte(out))
		if len(src.ids) == 0 {
			src.err = fmt.Errorf("agy models: no model lines found")
		}
	case "claude":
		src.err = fmt.Errorf("%s", claudeSkipReason)
	default:
		src.err = fmt.Errorf("no CLI model list for this id format")
	}
	return src
}

type modelChange struct {
	alias, old, new string
	model           Model
}

// discoverChanges finds a newer id per alias; skipped aliases are reported, not failed.
func discoverChanges(ctx context.Context, models map[string]string, out io.Writer) []modelChange {
	sources := map[string]modelSource{}
	var aliases []string
	for alias := range models {
		aliases = append(aliases, alias)
	}
	slices.Sort(aliases)
	var changes []modelChange
	for _, alias := range aliases {
		current := models[alias]
		name := sourceFor(current)
		src, ok := sources[name]
		if !ok {
			src = loadSource(ctx, name)
			sources[name] = src
		}
		if src.err != nil {
			fmt.Fprintf(out, "%s: skip (%s): %v\n", alias, current, src.err)
			continue
		}
		next := newestModelID(current, src.ids)
		if next == "" {
			fmt.Fprintf(out, "%s: %s is current\n", alias, current)
			continue
		}
		m, ok := catalogModelForAlias(alias)
		if !ok {
			fmt.Fprintf(out, "%s: skip: no catalog key uses this model name\n", alias)
			continue
		}
		fmt.Fprintf(out, "%s: candidate %s -> %s\n", alias, current, next)
		changes = append(changes, modelChange{alias: alias, old: current, new: next, model: m})
	}
	return changes
}

func catalogModelForAlias(alias string) (Model, bool) {
	for _, m := range catalog {
		if m.Name == alias {
			return m, true
		}
	}
	return Model{}, false
}

// refreshProbe makes the same live call as `doctor --probe-models`, pinned to the candidate id.
func refreshProbe(_ context.Context, m Model, id string) error {
	dir, err := os.MkdirTemp("", "orch-refresh-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	m.resolvedID, m.resolvedSource = id, "refresh"
	_, problems, err := probeModelOnce(dir, 0, m)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", probeExcerpt(strings.Join(problems, "; ")))
	}
	return nil
}

func probeChanges(ctx context.Context, changes []modelChange, out io.Writer) []modelChange {
	var ok []modelChange
	for _, c := range changes {
		if err := refreshHooks.probe(ctx, c.model, c.new); err != nil {
			fmt.Fprintf(out, "%s: probe %s fail: %v (not applied)\n", c.alias, c.new, err)
			continue
		}
		fmt.Fprintf(out, "%s: probe %s ok\n", c.alias, c.new)
		ok = append(ok, c)
	}
	return ok
}

func refreshRun(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	data, err := cmd.Output()
	if err != nil {
		detail := ""
		if ee, ok := err.(*exec.ExitError); ok {
			detail = ": " + probeExcerpt(string(ee.Stderr))
		}
		return string(data), fmt.Errorf("%s %s: %w%s", name, strings.Join(args, " "), err, detail)
	}
	return string(data), nil
}

func gitOut(ctx context.Context, repo string, args ...string) (string, error) {
	out, err := refreshHooks.run(ctx, repo, "git", args...)
	return strings.TrimSpace(out), err
}

// repoSkipReason returns why the checkout must not be touched, or "" when it is safe.
func repoSkipReason(ctx context.Context, repo string) (string, error) {
	status, err := gitOut(ctx, repo, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "working tree is dirty", nil
	}
	branch, err := gitOut(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	def := "main"
	if head, err := gitOut(ctx, repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && head != "" {
		def = strings.TrimPrefix(head, "origin/")
	}
	if branch != def {
		return fmt.Sprintf("on branch %s, not default branch %s", branch, def), nil
	}
	if updateLiveWriters(repo) > 0 {
		return "live orch writer in checkout", nil
	}
	if _, err := gitOut(ctx, repo, "fetch", "--quiet"); err != nil {
		return "git fetch failed: " + err.Error(), nil
	}
	counts, err := gitOut(ctx, repo, "rev-list", "--left-right", "--count", "@{u}...HEAD")
	if err != nil {
		return "no upstream to compare with", nil
	}
	f := strings.Fields(counts)
	if len(f) != 2 {
		return "unreadable upstream comparison: " + counts, nil
	}
	behind, ahead := f[0] != "0", f[1] != "0"
	switch {
	case behind && ahead:
		return "diverged from upstream", nil
	case behind:
		return "behind upstream", nil
	case ahead:
		return "ahead of upstream (unpushed commits)", nil
	}
	return "", nil
}

func readRepoModels(repo string) ([]byte, map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(repo, "models.yaml"))
	if err != nil {
		return nil, nil, err
	}
	mf, err := loadManifest(data)
	if err != nil {
		return nil, nil, fmt.Errorf("models.yaml: %w", err)
	}
	return data, mf.Models, nil
}

// rewriteModelsYAML replaces only the alias lines, keeping comments and order.
func rewriteModelsYAML(data []byte, changes []modelChange) ([]byte, error) {
	text := string(data)
	for _, c := range changes {
		re := regexp.MustCompile(`(?m)^(  ` + regexp.QuoteMeta(c.alias) + `: )` + regexp.QuoteMeta(c.old) + `[ \t]*$`)
		if n := len(re.FindAllStringIndex(text, -1)); n != 1 {
			return nil, fmt.Errorf("models.yaml: expected one line for %s: %s, found %d", c.alias, c.old, n)
		}
		text = re.ReplaceAllString(text, "${1}"+c.new)
	}
	if _, err := loadManifest([]byte(text)); err != nil {
		return nil, fmt.Errorf("rewritten models.yaml: %w", err)
	}
	return []byte(text), nil
}

var decisionDateLineRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}: .* upgraded from `)

// decisionLine follows the existing "2026-11-19: Sol upgraded from a to b." form.
func decisionLine(date string, changes []modelChange) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, fmt.Sprintf("%s upgraded from %s to %s", c.model.Tier, c.old, c.new))
	}
	return date + ": " + strings.Join(parts, "; ") + "."
}

// insertDecision adds the line after the last model upgrade note, or after the D20 probe paragraph.
func insertDecision(text, line string) (string, error) {
	lines := strings.Split(text, "\n")
	at := -1
	for i, l := range lines {
		if decisionDateLineRe.MatchString(l) {
			at = i
		}
	}
	if at < 0 {
		for i, l := range lines {
			if strings.Contains(l, "doctor --probe-models") {
				at = i
				break
			}
		}
		if at < 0 {
			return "", fmt.Errorf("decisions.md: no place for the model note")
		}
		lines = slices.Insert(lines, at+1, "")
		at++
	}
	lines = slices.Insert(lines, at+1, "", line)
	return strings.Join(lines, "\n"), nil
}

func writeDecision(repo string, changes []modelChange) error {
	path := filepath.Join(repo, "skill", "references", "decisions.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next, err := insertDecision(string(data), decisionLine(refreshHooks.now().Format("2006-01-02"), changes))
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(next), 0644)
}

func refreshCommitSubject(changes []modelChange) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, c.old+" -> "+c.new)
	}
	return "chore: update model ids (" + strings.Join(parts, ", ") + ")"
}

// applyChanges edits files, regenerates docs from the new manifest and runs catalog tests.
// Docs run in a fresh `go run` because this binary embeds the old models.yaml.
func applyChanges(ctx context.Context, repo string, data []byte, changes []modelChange, out io.Writer) error {
	next, err := rewriteModelsYAML(data, changes)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(repo, "models.yaml"), next, 0644); err != nil {
		return err
	}
	if err := writeDecision(repo, changes); err != nil {
		return err
	}
	steps := [][]string{
		{"go", "run", ".", "docs", "gen", "--root", "."},
		{"go", "test", "-count=1", "-run", refreshTestPattern, "."},
	}
	for _, s := range steps {
		fmt.Fprintf(out, "run: %s\n", strings.Join(s, " "))
		if _, err := refreshHooks.run(ctx, repo, s[0], s[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func restoreRepo(ctx context.Context, repo string, out io.Writer) {
	if _, err := gitOut(ctx, repo, "checkout", "--", "."); err != nil {
		fmt.Fprintf(out, "restore failed, inspect %s: %v\n", repo, err)
	}
}

func publishChanges(ctx context.Context, repo string, changes []modelChange, out io.Writer) error {
	subject := refreshCommitSubject(changes)
	if _, err := gitOut(ctx, repo, "commit", "-q", "-am", subject); err != nil {
		return err
	}
	fmt.Fprintf(out, "committed: %s\n", subject)
	if _, err := gitOut(ctx, repo, "push"); err != nil {
		return fmt.Errorf("committed but push failed: %w", err)
	}
	fmt.Fprintln(out, "pushed")
	if _, err := refreshHooks.run(ctx, repo, "sh", "-c", refreshInstallShell); err != nil {
		return fmt.Errorf("pushed but reinstall failed: %w", err)
	}
	fmt.Fprintln(out, "reinstalled")
	return nil
}

// modelsRefreshCommand parses `models refresh [--dry-run] [--repo DIR]`.
func modelsRefreshCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("models refresh", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "print candidates and probe results only")
	repo := fs.String("repo", ".", "orch checkout to update")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "orch: usage: orch models refresh [--dry-run] [--repo DIR]")
		return 2
	}
	// `models` is worker-readable, but refresh commits and pushes.
	if envOrch("ROLE") == "worker" {
		fmt.Fprintln(stderr, "orch: ORCH_ROLE=worker: workers cannot refresh models (Head only)")
		return 1
	}
	if err := refreshModels(context.Background(), modelsRefreshOptions{Repo: *repo, DryRun: *dryRun, Out: stdout}); err != nil {
		fmt.Fprintln(stderr, "orch:", err)
		return 1
	}
	return 0
}

// refreshModels updates models.yaml aliases to the newest CLI-listed ids that answer a live probe.
func refreshModels(ctx context.Context, opts modelsRefreshOptions) error {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	repo := opts.Repo
	if repo == "" {
		repo = "."
	}
	reason, err := repoSkipReason(ctx, repo)
	if err != nil {
		return err
	}
	if reason != "" && !opts.DryRun {
		fmt.Fprintf(out, "skip: %s (%s)\n", reason, repo)
		return nil
	}
	if reason != "" {
		fmt.Fprintf(out, "note: a real refresh would skip: %s\n", reason)
	}
	data, models, err := readRepoModels(repo)
	if err != nil {
		return err
	}
	changes := probeChanges(ctx, discoverChanges(ctx, models, out), out)
	if opts.DryRun || len(changes) == 0 {
		if len(changes) == 0 {
			fmt.Fprintln(out, "no verified model id changes")
		}
		return nil
	}
	if err := applyChanges(ctx, repo, data, changes, out); err != nil {
		restoreRepo(ctx, repo, out)
		return err
	}
	return publishChanges(ctx, repo, changes, out)
}
