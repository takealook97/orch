package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fakeCR(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "bin ' $directory")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cr")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func TestPaneAccountPolicy(t *testing.T) {
	path := fakeCR(t)
	t.Setenv("CR_LAUNCH_EMAIL", "inherited@example.test")
	t.Setenv("CR_HOME", "/registry with 'quotes")
	for _, kind := range []string{"codex", "claude"} {
		for _, mode := range []string{"", "auto", "cr"} {
			acc, got, err := paneAccount(mode, kind, nil)
			if err != nil || got != path || acc.Label != "cr" || !envHas(acc.Env, "CR_LAUNCH_EMAIL=") || !envHas(acc.Env, "CR_HOME=/registry with 'quotes") {
				t.Fatalf("%s/%s: %+v %q %v", mode, kind, acc, got, err)
			}
		}
	}
	acc, got, err := paneAccount("same", "codex", nil)
	if err != nil || got != "" || acc.Label == "cr" {
		t.Fatalf("same: %+v %s %v", acc, got, err)
	}
	if _, _, err := paneAccount("cr", "agy", nil); err == nil {
		t.Fatal("unsupported CR accepted")
	}
	acc, got, err = paneAccount("auto", "agy", nil)
	if err != nil || got != "" || acc.Label == "cr" {
		t.Fatal("non-CR provider changed")
	}
	t.Setenv("ORCH_ACCOUNT_CMD", "printf 'LABEL=explicit-hook\\n'")
	acc, got, err = paneAccount("hook", "codex", nil)
	if err != nil || got != "" || acc.Label != "explicit-hook" {
		t.Fatalf("hook: %+v %s %v", acc, got, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, _, err := paneAccount("cr", "codex", nil); err == nil {
		t.Fatal("missing executable accepted")
	}
	acc, got, err = paneAccount("auto", "codex", nil)
	if err != nil || got != "" || acc.Label == "cr" {
		t.Fatal("native fallback failed")
	}
}

func TestCRPaneCommandRoundTrip(t *testing.T) {
	path := fakeCR(t)
	// A real shell invokes only this fixture, proving all argv and cwd quoting.
	script := "#!/bin/sh\nprintf '%s\\n' \"${CR_LAUNCH_EMAIL-unset}\" \"$CR_HOME\" \"$ORCH_WORKER\" \"$PWD\" \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CR_LAUNCH_EMAIL", "must-not-pin")
	t.Setenv("HERDR_AGENT", "claude")
	cwd := filepath.Join(t.TempDir(), "cwd ' $(touch never) ;")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	m, _ := modelByKey("codex-high-m")
	launch, _ := launchArgs(m, false)
	env := []string{"CR_LAUNCH_EMAIL=", "HERDR_AGENT=", "CR_HOME=registry ' $home", "ORCH_WORKER=worker-one"}
	out, err := exec.Command("/bin/sh", "-c", crPaneCommand(path, m.Kind, cwd, env, launch)).CombinedOutput()
	if err != nil {
		t.Fatalf("shell: %v %s", err, out)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	want := append([]string{"unset", "registry ' $home", "worker-one", cwd, "codex"}, launch...)
	// macOS may canonicalize /var to /private/var in PWD.
	if len(lines) > 3 {
		real, _ := filepath.EvalSymlinks(cwd)
		if lines[3] == real {
			lines[3] = cwd
		}
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("argv/env:\n%q\nwant %q", lines, want)
	}
	if !slices.Contains(lines, "--no-alt-screen") || !slices.Contains(lines, `model_reasoning_effort="medium"`) {
		t.Fatal("catalog flags lost")
	}
}

func TestCRSpawnLifecycle(t *testing.T) {
	for _, mode := range []string{"ready", "wrong-pane", "missing", "rename-error", "name-missing"} {
		t.Run(mode, func(t *testing.T) {
			r, f := overflowFixture(t)
			path := fakeCR(t)
			t.Setenv("CR_HOME", "registry-root")
			gets, renamed, ran := 0, false, false
			herdrRun = func(args ...string) ([]byte, error) {
				switch strings.Join(args[:2], " ") {
				case "pane run":
					ran = true
					if args[2] != "pane-1" || !strings.Contains(args[3], shellArg(path)) || !strings.Contains(args[3], "'CR_HOME=registry-root'") {
						t.Fatalf("launch: %v", args)
					}
					return []byte(`{"result":{}}`), nil
				case "agent get":
					if args[2] == "cr-worker" && mode == "name-missing" {
						return nil, errors.New("name not registered")
					}
					if args[2] == "pane-1" {
						gets++
						if mode == "missing" || gets < 3 {
							return nil, errors.New("not detected")
						}
						if mode == "wrong-pane" {
							return []byte(`{"result":{"agent":{"pane_id":"user-pane"}}}`), nil
						}
						return []byte(`{"result":{"agent":{"pane_id":"pane-1","agent_status":"idle"}}}`), nil
					}
				case "agent rename":
					if args[2] != "pane-1" || args[3] != "cr-worker" {
						t.Fatalf("rename: %v", args)
					}
					if mode == "rename-error" {
						return nil, errors.New("rename failed")
					}
					renamed = true
					f.agents[args[3]] = args[2]
					return []byte(`{"result":{}}`), nil
				}
				return f.run(args...)
			}
			w, err := spawn(spawnOpts{Run: r.ID, Key: "codex-high-m", Name: "cr-worker", Role: roleProduce})
			if mode == "ready" {
				if err != nil || w.Account != "cr" || !ran || !renamed || gets < 3 {
					t.Fatalf("startup: %+v %v ran=%v renamed=%v gets=%d", w, err, ran, renamed, gets)
				}
				if err := closeWorker(r.ID, w.Name); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || w != nil {
					t.Fatalf("failure accepted: %+v %v", w, err)
				}
				saved, _ := loadRun(r.ID)
				if saved.worker("cr-worker").Pane != "" || len(f.tabs["head-tab"]) != 1 {
					t.Fatal("failed CR pane not cleaned")
				}
			}
		})
	}
}

func TestCRFallbackSkipsHook(t *testing.T) {
	r, f := overflowFixture(t)
	fakeCR(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	t.Setenv("ORCH_ACCOUNT_CMD", "touch "+shellArg(marker)+"; printf 'LABEL=bad\\n'")
	cwd := t.TempDir()
	contract := r.contractPath("original")
	if err := os.WriteFile(contract, []byte("# Contract: original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := withRun(r.ID, func(r *Run) error {
		r.Workers = append(r.Workers, &Worker{Name: "original", Key: "codex-high-m", Kind: "codex", Family: familyOpenAI, Role: roleProduce, Purpose: "impl", Account: "cr", Cwd: cwd, Contract: contract, Tried: []string{"codex-high-m"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Stop at CR's pane command. This proves fallback selected CR and advanced the
	// model, without writing a Claude trust file or invoking a provider.
	herdrRun = func(args ...string) ([]byte, error) {
		if strings.Join(args[:2], " ") == "pane run" {
			return nil, errors.New("fixture launch stop")
		}
		return f.run(args...)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if _, err := doFallback(r.ID, "original", "quota"); err == nil || !strings.Contains(err.Error(), "fixture launch stop") {
		t.Fatalf("fallback: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("CR fallback invoked account hook")
	}
	saved, _ := loadRun(r.ID)
	replacement := saved.worker("original-f1")
	if replacement == nil || replacement.Account != "cr" || replacement.Key != "claude-high-m" || replacement.Pane != "" {
		t.Fatalf("replacement: %+v", replacement)
	}
}
