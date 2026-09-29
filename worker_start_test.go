package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func startupTest(t *testing.T) {
	t.Helper()
	oldRun, oldTimings, oldReaper := herdrRun, timings, startReaper
	t.Cleanup(func() { herdrRun, timings, startReaper = oldRun, oldTimings, oldReaper })
	timings.ReadyTimeout = 8 * time.Millisecond
	timings.ReadyPoll = time.Millisecond
	timings.TrustSettleStart = 0
	timings.TrustSettleReady = 0
	timings.StartRetry = 0
	startReaper = func(string) error { return nil }
}

// Herdr can acknowledge start without ever registering the requested agent.
func TestSpawnUnregisteredCustomCwdClosesPane(t *testing.T) {
	startupTest(t)
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "head")
	cwd := t.TempDir() // outside the Head cwd
	cwd, _ = filepath.EvalSymlinks(cwd)
	r, err := createRun("cwd regression", "light", "", "")
	if err != nil {
		t.Fatal(err)
	}
	live, closed, pretrusted := false, false, false
	herdrRun = func(args ...string) ([]byte, error) {
		switch strings.Join(args[:2], " ") {
		case "pane layout":
			if !live {
				return []byte(`{"result":{"layout":{"area":{"width":90,"height":30},"panes":[{"pane_id":"head","rect":{"width":90,"height":30}}]}}}`), nil
			}
			return []byte(`{"result":{"layout":{"area":{"width":90,"height":30},"panes":[{"pane_id":"head","rect":{"width":30,"height":30}},{"pane_id":"worker-pane","rect":{"x":30,"width":60,"height":30}}]}}}`), nil
		case "pane split":
			live = true
			if !slices.Contains(args, cwd) || !slices.Contains(args, "--no-focus") {
				t.Fatalf("split args: %v", args)
			}
			return []byte(`{"result":{"pane":{"pane_id":"worker-pane"}}}`), nil
		case "agent start":
			pretrusted = slices.Contains(args, fmt.Sprintf(`projects.%q.trust_level="trusted"`, cwd))
			return []byte(`{"result":{}}`), nil
		case "agent get":
			return nil, errors.New("agent_not_found")
		case "pane read":
			return []byte(`{"result":{"text":"Do you trust the files in this directory?"}}`), nil
		case "pane close":
			if args[2] != "worker-pane" {
				t.Fatalf("closed unrelated pane: %v", args)
			}
			live, closed = false, true
			return []byte(`{"result":{}}`), nil
		case "pane list":
			if live {
				return []byte(`{"result":{"panes":[{"pane_id":"head"},{"pane_id":"worker-pane"}]}}`), nil
			}
			return []byte(`{"result":{"panes":[{"pane_id":"head"}]}}`), nil
		}
		t.Fatalf("unexpected Herdr call: %v", args)
		return nil, nil
	}
	w, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: "cwd-probe", Role: roleProduce, Cwd: cwd})
	if err == nil || w != nil {
		t.Fatalf("spawn silently succeeded with no registration: worker=%+v err=%v", w, err)
	}
	if !strings.Contains(err.Error(), "folder-trust dialog") || !strings.Contains(err.Error(), "agent_not_found") || !strings.Contains(err.Error(), cwd) {
		t.Fatalf("missing diagnosis: %v", err)
	}
	if !pretrusted || !closed || live {
		t.Fatalf("pretrusted=%v closed=%v live=%v", pretrusted, closed, live)
	}
	spawnErr := err
	r, err = loadRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved := r.worker("cwd-probe")
	if saved.Status != "failed" || saved.Pane != "" || saved.Closed.IsZero() {
		t.Fatalf("failed worker not cleaned: %+v", saved)
	}
	t.Logf("mock reproduction: agent start success; agent get agent_not_found; spawn error: %v; pane removal confirmed", spawnErr)
}

func TestStartWorkerAgentWaitsAfterSuccessfulStartAndRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			startupTest(t)
			starts, gets := 0, 0
			herdrRun = func(args ...string) ([]byte, error) {
				switch strings.Join(args[:2], " ") {
				case "agent start":
					starts++
					if retry && starts == 1 {
						return nil, errors.New("shell not ready")
					}
					return []byte(`{"result":{}}`), nil
				case "agent get":
					gets++
					if gets < 3 {
						return nil, errors.New("agent_not_found")
					}
					return []byte(`{"result":{"agent":{"pane_id":"p","agent_status":"idle"}}}`), nil
				case "pane read":
					return []byte(`{"result":{"text":"shell prompt"}}`), nil
				}
				t.Fatalf("unexpected call: %v", args)
				return nil, nil
			}
			if err := startWorkerAgent("probe", "p", "/cwd", []string{"agent", "start", "probe"}); err != nil {
				t.Fatal(err)
			}
			want := 1
			if retry {
				want = 2
			}
			if starts != want || gets != 3 {
				t.Fatalf("starts=%d gets=%d", starts, gets)
			}
		})
	}
}

func TestRegistrationRejectsWrongOrMissingPane(t *testing.T) {
	for _, pane := range []string{"other", ""} {
		t.Run(pane, func(t *testing.T) {
			startupTest(t)
			herdrRun = func(args ...string) ([]byte, error) {
				if args[0] == "agent" {
					return []byte(fmt.Sprintf(`{"result":{"pane_id":%q,"agent_status":"idle"}}`, pane)), nil
				}
				return []byte(`{"result":{"text":"shell prompt"}}`), nil
			}
			err := waitAgentRegistered("probe", "p", "/cwd", timings.ReadyTimeout)
			if err == nil {
				t.Fatal("registration without the expected pane accepted")
			}
			if pane == "" && !strings.Contains(err.Error(), "no pane_id") {
				t.Fatal(err)
			}
			if pane != "" && !strings.Contains(err.Error(), "expected p") {
				t.Fatal(err)
			}
		})
	}
}

func TestStartWorkerAgentNotReadyWaitsForReadiness(t *testing.T) {
	startupTest(t)
	gets := 0
	herdrRun = func(args ...string) ([]byte, error) {
		if args[1] == "start" {
			return nil, errors.New("agent_not_ready")
		}
		gets++
		status := "starting"
		if gets >= 3 {
			status = "idle"
		}
		return []byte(fmt.Sprintf(`{"result":{"pane_id":"p","agent_status":%q}}`, status)), nil
	}
	if err := startWorkerAgent("probe", "p", "/cwd", []string{"agent", "start", "probe"}); err != nil {
		t.Fatal(err)
	}
	if gets != 3 {
		t.Fatalf("readiness not checked: gets=%d", gets)
	}
}

func TestPretrustExplicitCwdOnly(t *testing.T) {
	outside := t.TempDir()
	if trustable(outside) {
		t.Fatal("test folder must be outside Head cwd")
	}
	if args, err := pretrust("codex", outside, nil); err != nil || len(args) != 0 {
		t.Fatalf("implicit trust: %v %v", args, err)
	}
	args, err := pretrustLaunch("codex", outside, nil, true)
	if err != nil || len(args) != 2 || !strings.Contains(args[1], `trust_level="trusted"`) {
		t.Fatalf("explicit trust missing: %v %v", args, err)
	}
	// Account-local Claude configuration records only the selected directory.
	config := t.TempDir()
	_, err = pretrustLaunch("claude", outside, []string{"CLAUDE_CONFIG_DIR=" + config}, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(config, ".claude.json"))
	if err != nil || !strings.Contains(string(data), outside) {
		t.Fatalf("Claude selected directory trust: %s %v", data, err)
	}
}
