package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type recallHit struct {
	Path          string    `json:"path"`
	Date          time.Time `json:"date"`
	Kind          string    `json:"kind"`
	Lines         []string  `json:"lines"`
	MatchingLines int       `json:"matching_lines"`
}

func recallRepo(cwd string) string {
	if cwd == "" {
		return ""
	}
	root, err := cacheGit(cwd, "rev-parse", "--show-toplevel")
	if err == nil {
		return root
	}
	p, _ := filepath.Abs(cwd)
	return filepath.Clean(p)
}
func recallSearch(query, cwd string, all bool, limit int) ([]recallHit, error) {
	terms := strings.Fields(strings.ToLower(query))
	hits := []recallHit{}
	entries, err := os.ReadDir(runsDir())
	if os.IsNotExist(err) {
		return hits, nil
	}
	if err != nil {
		return nil, err
	}
	repo := recallRepo(cwd)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(runsDir(), entry.Name())
		var r Run
		b, _ := os.ReadFile(filepath.Join(dir, "run.json"))
		_ = json.Unmarshal(b, &r)
		var meta struct {
			Cwd  string `json:"cwd"`
			Repo string `json:"repo"`
		}
		_ = json.Unmarshal(b, &meta)
		runRepos := []string{meta.Cwd, meta.Repo}
		taskCwds := map[string]string{}
		candidates := map[string]string{}
		addTask := func(t *Task, p string) {
			if t != nil && (t.Kind == "ask" || t.Kind == "do") {
				taskCwds[p] = t.Cwd
				candidates[p] = t.Kind
				runRepos = append(runRepos, t.Cwd)
			}
		}
		for _, t := range r.Tasks {
			if t != nil {
				addTask(t, filepath.Join(dir, "tasks", t.ID, "answer.md"))
			}
		}
		tasks, _ := filepath.Glob(filepath.Join(dir, "tasks", "*", "task.json"))
		for _, p := range tasks {
			var t Task
			b, e := os.ReadFile(p)
			if e == nil && json.Unmarshal(b, &t) == nil {
				addTask(&t, filepath.Join(filepath.Dir(p), "answer.md"))
			}
		}
		resultRepos := map[string]string{}
		for _, w := range r.Workers {
			if w == nil {
				continue
			}
			root := w.Cwd
			if w.Worktree != "" {
				root = w.Worktree
			}
			runRepos = append(runRepos, root)
			resultRepos[filepath.Join(dir, "results", w.Name+".md")] = root
			if w.Result != "" {
				p := w.Result
				if !filepath.IsAbs(p) {
					p = filepath.Join(dir, p)
				}
				resultRepos[filepath.Clean(p)] = root
			}
		}
		matches := func(paths []string) bool {
			if all {
				return true
			}
			for _, p := range paths {
				if p != "" && recallRepo(p) == repo {
					return true
				}
			}
			return false
		}
		results, _ := filepath.Glob(filepath.Join(dir, "results", "*.md"))
		for _, p := range results {
			candidates[p] = "result"
		}
		candidates[filepath.Join(dir, "knowledge.md")] = "knowledge"
		candidates[filepath.Join(dir, "findings.md")] = "knowledge"
		for p, kind := range candidates {
			roots := runRepos
			if root, ok := taskCwds[p]; ok {
				roots = []string{root}
			} else if root, ok := resultRepos[p]; ok {
				roots = []string{root}
			}
			if !matches(roots) {
				continue
			}
			b, e := os.ReadFile(p)
			if e != nil {
				continue
			}
			lower := strings.ToLower(string(b))
			matched := len(terms) > 0
			for _, term := range terms {
				if !strings.Contains(lower, term) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			info, e := os.Stat(p)
			if e != nil {
				continue
			}
			hit := recallHit{Path: p, Kind: kind, Date: info.ModTime(), Lines: []string{}}
			for _, line := range strings.Split(string(b), "\n") {
				found := false
				for _, term := range terms {
					if strings.Contains(strings.ToLower(line), term) {
						found = true
						break
					}
				}
				if !found {
					continue
				}
				hit.MatchingLines++
				if len(hit.Lines) < 3 {
					runes := []rune(strings.TrimSpace(line))
					if len(runes) > 160 {
						runes = runes[:160]
					}
					hit.Lines = append(hit.Lines, string(runes))
				}
			}
			hits = append(hits, hit)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].MatchingLines != hits[j].MatchingLines {
			return hits[i].MatchingLines > hits[j].MatchingLines
		}
		if !hits[i].Date.Equal(hits[j].Date) {
			return hits[i].Date.After(hits[j].Date)
		}
		return hits[i].Path < hits[j].Path
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
func recallCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("orch recall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("limit", 10, "maximum hits")
	all := fs.Bool("all-repos", false, "search all repositories")
	asJSON := fs.Bool("json", false, "JSON output")
	pos, err := parse(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 || strings.TrimSpace(pos[0]) == "" || *limit < 1 {
		fmt.Fprintln(stderr, "usage: orch recall query [--limit N] [--all-repos] [--json]")
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	hits, err := recallSearch(pos[0], cwd, *all, *limit)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *asJSON {
		json.NewEncoder(stdout).Encode(hits)
	} else {
		for _, h := range hits {
			fmt.Fprintf(stdout, "%s  %s  %s\n", h.Path, h.Date.Format("2006-01-02"), h.Kind)
			for _, line := range h.Lines {
				fmt.Fprintf(stdout, "  %s\n", line)
			}
		}
	}
	return 0
}
