package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Pane policy is deliberately separate from the one-off account policy.
func paneAccount(mode, kind string, exclude []string) (Account, string, error) {
	switch mode {
	case "", "auto", "cr":
		supported := kind == "codex" || kind == "claude"
		if supported {
			if path, err := exec.LookPath("cr"); err == nil {
				path, err = filepath.Abs(path)
				if err != nil {
					return Account{}, "", fmt.Errorf("resolve cr executable: %w", err)
				}
				acc := Account{Label: "cr", Env: []string{"CR_LAUNCH_EMAIL=", "HERDR_AGENT="}}
				if home := os.Getenv("CR_HOME"); home != "" {
					acc.Env = append(acc.Env, "CR_HOME="+home)
				}
				return acc, path, nil
			}
		}
		if mode == "cr" {
			return Account{}, "", fmt.Errorf("--account cr requires Codex/Claude and an available cr executable")
		}
		return sameAccount(kind), "", nil
	case "same", "hook", "best":
		acc, err := pickAccount(mode, kind, exclude)
		return acc, "", err
	}
	return Account{}, "", fmt.Errorf("unknown --account %q (auto|cr|same|hook)", mode)
}

func shellArg(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func crPaneCommand(path, kind, cwd string, env, launch []string) string {
	// Clear inherited account pins and hints again at execution time: shell startup
	// files may have changed them after Herdr created the pane.
	argv := []string{"env", "-u", "CR_LAUNCH_EMAIL", "-u", "HERDR_AGENT", "-u", "CLAUDE_CODE_CHILD_SESSION"}
	for _, e := range childCLIEnv(env) {
		if !strings.HasPrefix(e, "CR_LAUNCH_EMAIL=") && !strings.HasPrefix(e, "HERDR_AGENT=") {
			argv = append(argv, e)
		}
	}
	argv = append(argv, path, kind)
	argv = append(argv, launch...)
	for i := range argv {
		argv[i] = shellArg(argv[i])
	}
	return "cd " + shellArg(cwd) + " && " + strings.Join(argv, " ")
}

// CR advertises its provider via HERDR_AGENT. Discover it through the owned pane,
// never through a provider name that might belong to a user's unrelated pane.
func startCRWorker(name, pane, cwd, command string) error {
	if _, err := herdrRun("pane", "run", pane, command); err != nil {
		return err
	}
	if err := waitAgentRegistered(pane, pane, cwd, timings.ReadyTimeout); err != nil {
		return err
	}
	if _, err := herdrRun("agent", "rename", pane, name); err != nil {
		return fmt.Errorf("rename CR agent: %w", err)
	}
	if err := waitAgentRegistered(name, pane, cwd, timings.ReadyTimeout); err != nil {
		return err
	}
	if err := waitReady(name, cwd, timings.ReadyTimeout); err != nil {
		return err
	}
	return confirmAgentLive(name, pane, cwd)
}

// Keep CR in charge of account rotation when advancing along a model chain.
func fallbackAccount(w *Worker) string {
	if w.Account == "cr" {
		return "cr"
	}
	return "same"
}

// childCLIEnv removes Claude's parent-session marker without changing account
// selection or CLAUDECODE. The supplied slice is never mutated.
func childCLIEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "CLAUDE_CODE_CHILD_SESSION=") {
			out = append(out, e)
		}
	}
	return out
}

// A legacy pane's shell is owned by Herdr, so sanitizing the orch process alone
// cannot clean it. Wait for the shell to acknowledge unset before agent start.
func clearPaneChildSession(pane string) error {
	const marker = "orch-child-session-cleared"
	// Split the marker so terminal input echo cannot satisfy wait-output.
	command := "unset CLAUDE_CODE_CHILD_SESSION && printf '%s%s\\n' 'orch-child-session-' 'cleared'"
	if _, err := herdrRun("pane", "run", pane, command); err != nil {
		return err
	}
	_, err := herdrRun("pane", "wait-output", pane, "--match", marker, "--timeout", "15000")
	return err
}
