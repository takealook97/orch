package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var hudClaudeProjectChar = regexp.MustCompile(`[^A-Za-z0-9]`)
var hudClaudeVersion = regexp.MustCompile(`^(.*)-([0-9]+)-([0-9]+)$`)
var hudClaudeDate = regexp.MustCompile(`-[0-9]{8}$`)

func hudModelDisplay(id string) string {
	if !strings.HasPrefix(id, "claude-") {
		return id
	}
	name := hudClaudeDate.ReplaceAllString(strings.TrimPrefix(id, "claude-"), "")
	if parts := hudClaudeVersion.FindStringSubmatch(name); parts != nil {
		return parts[1] + " " + parts[2] + "." + parts[3]
	}
	return name
}

// Read a bounded tail only; incomplete JSON written by the running agent is ignored.
func hudClaudeTranscriptModel(home, cwd string) string {
	if cwd == "" {
		return ""
	}
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(user, ".claude")
	}
	dir := filepath.Join(home, "projects", hudClaudeProjectChar.ReplaceAllString(cwd, "-"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var modified time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if newest == "" || info.ModTime().After(modified) {
			newest, modified = entry.Name(), info.ModTime()
		}
	}
	if newest == "" {
		return ""
	}
	f, err := os.Open(filepath.Join(dir, newest))
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	offset := info.Size() - 256*1024
	if offset < 0 {
		offset = 0
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(f, 256*1024))
	if err != nil {
		return ""
	}
	lines := bytes.Split(raw, []byte{'\n'})
	// A tail starting mid-record cannot be trusted as a complete JSONL record.
	if offset > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var record struct {
			Model   string `json:"model"`
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &record) != nil {
			continue
		}
		for _, id := range []string{record.Message.Model, record.Model} {
			if strings.HasPrefix(id, "claude-") {
				return id
			}
		}
	}
	return ""
}
