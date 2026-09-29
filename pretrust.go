package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Folder trust is granted before a worker starts, so no CLI shows its trust dialog.
// It is granted only where the Head itself already works: its cwd and below, or a git
// worktree of the Head's repository. An explicit worker cwd also grants trust for
// that launch; unrelated folders keep the CLI's own dialog.

// trustable reports whether orch may pre-trust dir on the Head's behalf.
func trustable(dir string) bool {
	if withinHeadCwd(dir) {
		return true
	}
	head, err := os.Getwd()
	if err != nil {
		return false
	}
	out, err := exec.Command("git", "-C", head, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if wt, ok := strings.CutPrefix(line, "worktree "); ok && within(wt, dir) {
			return true
		}
	}
	return false
}

// pretrust prepares trust for a worker of kind in dir and returns extra CLI arguments.
func pretrust(kind, dir string, env []string) ([]string, error) {
	return pretrustLaunch(kind, dir, env, false)
}

// pretrustLaunch treats an explicitly selected worker directory as launch authorization.
// The caller resolves symlinks before passing it; this does not extend runtime trust scope.
func pretrustLaunch(kind, dir string, env []string, explicitCwd bool) ([]string, error) {
	if !explicitCwd && !trustable(dir) {
		return nil, nil
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real // record the real location only
	}
	switch kind {
	case "codex":
		// A per-launch override; nothing is written to any config file.
		return []string{"-c", fmt.Sprintf(`projects.%q.trust_level="trusted"`, dir)}, nil
	case "claude":
		// Inside a git repository Claude checks the repository root, not ancestors, so both
		// the folder and its git root are recorded explicitly.
		file := claudeConfigFile(env)
		dirs := []string{dir}
		if root, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output(); err == nil {
			if r := strings.TrimSpace(string(root)); r != "" && r != dir {
				dirs = append(dirs, r)
			}
		}
		for _, d := range dirs {
			if err := trustClaude(file, d); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case "agy":
		home, _ := os.UserHomeDir()
		return nil, trustAgy(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), dir)
	}
	return nil, nil
}

func claudeConfigFile(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "CLAUDE_CONFIG_DIR="); ok && v != "" {
			return filepath.Join(v, ".claude.json")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

// trustClaude records trust for exactly dir. Ancestor trust is not enough inside a git
// repository. The file is shared with running sessions, so it is rewritten only when needed.
func trustClaude(file, dir string) error {
	var cfg map[string]any
	data, err := os.ReadFile(file)
	switch {
	case errors.Is(err, os.ErrNotExist):
		cfg = map[string]any{}
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	projects, _ := cfg["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	if p, ok := projects[dir].(map[string]any); ok && p["hasTrustDialogAccepted"] == true {
		return nil // already recorded for exactly this folder
	}
	entry, _ := projects[dir].(map[string]any)
	if entry == nil {
		entry = map[string]any{"allowedTools": []any{}}
	}
	entry["hasTrustDialogAccepted"] = true
	projects[dir] = entry
	cfg["projects"] = projects
	return writeJSONAtomic(file, cfg)
}

// trustAgy adds dir to agy's trustedWorkspaces.
func trustAgy(file, dir string) error {
	var cfg map[string]any
	data, err := os.ReadFile(file)
	switch {
	case errors.Is(err, os.ErrNotExist):
		cfg = map[string]any{}
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
	}
	list, _ := cfg["trustedWorkspaces"].([]any)
	for _, v := range list {
		if s, _ := v.(string); s == dir {
			return nil
		}
	}
	cfg["trustedWorkspaces"] = append(list, dir)
	return writeJSONAtomic(file, cfg)
}

func writeJSONAtomic(file string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(file); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".orch-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
