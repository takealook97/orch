package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type askCacheEntry struct {
	Dir        string    `json:"task_dir"`
	AnswerPath string    `json:"answer_path"`
	Created    time.Time `json:"created"`
}

func askCacheTTL() time.Duration {
	hours := 168.0
	if s := envOrch("ASK_CACHE_TTL"); s != "" {
		if n, e := strconv.ParseFloat(s, 64); e == nil && n >= 0 {
			hours = n
		}
	}
	return time.Duration(hours * float64(time.Hour))
}
func cacheGit(cwd string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", cwd}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}
func askCacheKey(question, model string, maxOutput int, cwd string) string {
	if askCacheTTL() <= 0 {
		return ""
	}
	root, err := cacheGit(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	head, err := cacheGit(root, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	diff, err := cacheGit(root, "diff", "--binary", "HEAD", "--")
	if err != nil {
		return ""
	}
	untracked, err := cacheGit(root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return ""
	}
	// Include untracked contents as well: changing a newly created source file must invalidate research.
	h := sha256.New()
	h.Write([]byte(diff))
	h.Write([]byte{0})
	h.Write([]byte(untracked))
	for _, p := range strings.Split(untracked, "\x00") {
		if p != "" {
			b, e := os.ReadFile(filepath.Join(root, p))
			if e != nil {
				return ""
			}
			h.Write(b)
			h.Write([]byte{0})
		}
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	relCwd, err := filepath.Rel(root, filepath.Clean(absCwd))
	if err != nil {
		return ""
	}
	data, _ := json.Marshal([]any{"raw-question-v2", question, model, maxOutput, root, relCwd, head, fmt.Sprintf("%x", h.Sum(nil))})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func askCachePath(key string) string { return filepath.Join(orchHome(), "cache", "ask", key+".json") }
func writeAskCacheEntry(key string, e askCacheEntry) {
	if key == "" {
		return
	}
	p := askCachePath(key)
	if os.MkdirAll(filepath.Dir(p), 0700) != nil {
		return
	}
	b, _ := json.Marshal(e)
	f, err := os.CreateTemp(filepath.Dir(p), ".cache-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return
	}
	if f.Close() == nil {
		os.Rename(f.Name(), p)
	}
}
func storeAskCache(key string, t *Task) {
	if key == "" || askCacheTTL() <= 0 || t.Status != "done" || t.ExitCode != 0 {
		return
	}
	p := filepath.Join(t.Dir, "answer.md")
	answer, err := os.ReadFile(p)
	if err != nil || providerFailure(nil, string(answer)) != "" {
		return
	}
	if raw, err := os.ReadFile(filepath.Join(t.Dir, "output.log")); err == nil && providerFailure(raw, string(answer)) != "" {
		return
	}
	writeAskCacheEntry(key, askCacheEntry{t.Dir, p, time.Now()})
}
func lookupAskCache(key string) *Task {
	if key == "" || askCacheTTL() <= 0 {
		return nil
	}
	b, err := os.ReadFile(askCachePath(key))
	if err != nil {
		return nil
	}
	var e askCacheEntry
	if json.Unmarshal(b, &e) != nil || e.Created.IsZero() || time.Since(e.Created) >= askCacheTTL() {
		return nil
	}
	answer, err := os.ReadFile(e.AnswerPath)
	if err != nil || providerFailure(nil, string(answer)) != "" {
		return nil
	}
	if raw, err := os.ReadFile(filepath.Join(e.Dir, "output.log")); err == nil && providerFailure(raw, string(answer)) != "" {
		return nil
	}
	var t Task
	b, err = os.ReadFile(filepath.Join(e.Dir, "task.json"))
	if err != nil || json.Unmarshal(b, &t) != nil || t.Status != "done" || t.ExitCode != 0 {
		return nil
	}
	t.Dir = e.Dir
	t.AnswerPath = e.AnswerPath
	t.Answer = string(answer)
	t.Status = "done"
	return &t
}
func cachedAskReport(t *Task, maxOutput int, asJSON bool) string {
	t.Answer, t.Truncated = capText(t.Answer, maxOutput, t.AnswerPath)
	report := taskReport(t, asJSON)
	notice := t.Dir + " (rerun with --fresh)"
	if asJSON {
		var v map[string]any
		json.Unmarshal([]byte(report), &v)
		v["cached"] = notice
		b, _ := json.Marshal(v)
		return string(b)
	}
	return report + "\ncached: " + notice
}
