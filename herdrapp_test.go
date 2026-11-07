package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func appTest(t *testing.T) {
	t.Helper()
	t.Setenv("ORCH_MUX", "app")
	t.Setenv("HC_TERMINAL_ID", "th")
	t.Setenv("ORCH_HOME", t.TempDir())
	oldHC, oldApp, oldRPC, oldTiming := hcRun, appRun, hcRPC, timings
	t.Cleanup(func() {
		hcRun, appRun, hcRPC, timings = oldHC, oldApp, oldRPC, oldTiming
		headLocation.Lock()
		headLocation.terminal = ""
		headLocation.Unlock()
	})
	headLocation.Lock()
	headLocation.terminal = ""
	headLocation.Unlock()
	timings.ReadyTimeout = 20 * time.Millisecond
	timings.ReadyPoll = time.Millisecond
	timings.KeyGap = 0
}
func appLS(terminals string) []byte {
	return []byte(`{"spaces":[{"id":"s","tabs":[{"id":"tab","terminals":[` + terminals + `]}]}]}`)
}
func TestAppLaunchAndLookup(t *testing.T) {
	appTest(t)
	t.Setenv("PATH", "/x/bin:/usr/bin")
	calls := 0
	appRun = func(args ...string) ([]byte, error) {
		if !reflect.DeepEqual(args, []string{"ls"}) {
			t.Fatalf("app args %v", args)
		}
		calls++
		if calls == 1 {
			return appLS(`{"terminal":"th","pane":"ph"}`), nil
		}
		return appLS(`{"terminal":"th","pane":"ph"},{"terminal":"tw","pane":"pw"}`), nil
	}
	hcRun = func(args ...string) ([]byte, error) {
		want := []string{"run", "--bg", "--json", "--no-parent", "--no-notify", "--cwd", "/cwd", "--label", "herdr.space=s", "--label", "herdr.tab=tab", "--label", "orch.run=run", "--label", "orch.worker=w", "--", "env", "-u", "CLAUDE_CODE_CHILD_SESSION", "ORCH_ROLE=worker", "ORCH_RUN=run", "ORCH_WORKER=w", "ORCH_MODEL=codex-high-l", "CODEX_HOME=/account", "PATH=/x/bin:/usr/bin", "codex", "--no-daemon"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("argv\n%v\nwant\n%v", args, want)
		}
		return []byte(`{"address":"tw","state":"live"}`), nil
	}
	if headSpace() != "s" || headTab() != "tab" {
		t.Fatal("Head location")
	}
	terminal, pane, err := appLaunch(&Run{ID: "run", Workspace: "s"}, "tab", "/cwd", "w", []string{"CLAUDE_CODE_CHILD_SESSION=override", "ORCH_ROLE=worker", "ORCH_RUN=run", "ORCH_WORKER=w", "ORCH_MODEL=codex-high-l", "CODEX_HOME=/account"}, []string{"codex", "--no-daemon"})
	if err != nil || terminal != "tw" || pane != "pw" {
		t.Fatalf("launch %s %s %v", terminal, pane, err)
	}
	if calls != 2 {
		t.Fatalf("lookup/cache calls=%d", calls)
	}
}

func TestAppLaunchPassesHeadPath(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  []string
		want string
	}{
		{"inherited", []string{"ORCH_ROLE=worker"}, "PATH=/x/bin:/usr/bin"},
		{"explicit", []string{"PATH=/keep", "ORCH_ROLE=worker"}, "PATH=/keep"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			appTest(t)
			t.Setenv("PATH", "/x/bin:/usr/bin")
			var captured []string
			hcRun = func(args ...string) ([]byte, error) {
				captured = append([]string(nil), args...)
				return []byte(`{"address":"t9","state":"live"}`), nil
			}
			appRun = func(args ...string) ([]byte, error) {
				if !reflect.DeepEqual(args, []string{"ls"}) {
					t.Fatalf("app args %v", args)
				}
				return appLS(`{"terminal":"t9","pane":"p9"}`), nil
			}
			terminal, pane, err := appLaunch(&Run{ID: "run", Workspace: "s"}, "tab", "/cwd", "w", tt.env, []string{"codex", "--no-daemon"})
			if err != nil || terminal != "t9" || pane != "p9" {
				t.Fatalf("launch %s %s %v", terminal, pane, err)
			}
			unset := slices.Index(captured, "-u")
			path := slices.Index(captured, tt.want)
			argv := slices.Index(captured, "codex")
			count := 0
			for _, arg := range captured {
				if strings.HasPrefix(arg, "PATH=") {
					count++
				}
			}
			if unset < 0 || unset+1 >= len(captured) || captured[unset+1] != "CLAUDE_CODE_CHILD_SESSION" || path <= unset+1 || path >= argv || count != 1 {
				t.Fatalf("invalid PATH placement or duplication: %v", captured)
			}
		})
	}
}

func TestAppWaitReadyFailsFastOnFinishedTerminal(t *testing.T) {
	appTest(t)
	timings.ReadyTimeout = 5 * time.Second
	hcRun = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "get":
			return []byte(`{"address":"t9","state":"finished","exit_code":127,"reason":"exited"}`), nil
		case "read":
			return []byte("zsh: command not found: codex"), nil
		default:
			return nil, fmt.Errorf("unexpected %v", args)
		}
	}
	start := time.Now()
	err := appWaitReady("t9", "/cwd")
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("waited %s for a finished terminal", elapsed)
	}
	want := "terminal t9 exited before the agent registered (exit=127 reason=exited): zsh: command not found: codex"
	if err == nil || err.Error() != want {
		t.Fatalf("error=%v want %q", err, want)
	}
}

func TestAppWaitReadyTimeoutDiagnostics(t *testing.T) {
	for _, tt := range []struct {
		name, terminal, want string
		getErr               error
	}{
		{"live", `{"state":"live"}`, "terminal t9 not ready (login or folder trust may be blocking)", nil},
		{"get error", "", "terminal t9 not ready (login or folder trust may be blocking): get failed", errors.New("get failed")},
		{"unknown exit", `{"state":"finished","reason":"stopped"}`, "terminal t9 exited before the agent registered (exit=-1 reason=stopped): screen", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			appTest(t)
			timings.ReadyTimeout = 0
			hcRun = func(args ...string) ([]byte, error) {
				if args[0] == "get" {
					return []byte(tt.terminal), tt.getErr
				}
				if args[0] == "read" {
					return []byte("screen"), nil
				}
				return nil, fmt.Errorf("unexpected %v", args)
			}
			if err := appWaitReady("t9", "/cwd"); err == nil || err.Error() != tt.want {
				t.Fatalf("error=%v want %q", err, tt.want)
			}
		})
	}
}

func TestAppLaunchLookupFailureDiagnostics(t *testing.T) {
	appTest(t)
	lookupErr := errors.New("inventory unavailable")
	appRun = func(...string) ([]byte, error) { return nil, lookupErr }
	readBeforeClose := false
	hcRun = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "run":
			return []byte(`{"address":"t9"}`), nil
		case "get":
			return []byte(`{"address":"t9","state":"finished","exit_code":127,"reason":"exited","labels":{"orch.run":"run","orch.worker":"w"}}`), nil
		case "read":
			readBeforeClose = true
			return []byte("zsh: command not found: codex"), nil
		case "rm":
			if !readBeforeClose {
				t.Fatal("closed terminal before reading diagnostics")
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected %v", args)
		}
	}
	_, _, err := appLaunch(&Run{ID: "run", Workspace: "s"}, "tab", "/cwd", "w", nil, []string{"codex"})
	want := "app pane lookup: inventory unavailable (terminal t9 state=finished exit=127 reason=exited screen: zsh: command not found: codex)"
	if err == nil || err.Error() != want || !errors.Is(err, lookupErr) {
		t.Fatalf("error=%v want wrapped %q", err, want)
	}
}

func TestAppTerminalTail(t *testing.T) {
	appTest(t)
	for _, tt := range []struct {
		name, screen string
		n            int
		err          error
		want         string
	}{
		{"last nonempty lines", "first\n\nsecond\n \nthird\n", 2, nil, "second | third"},
		{"bounded", strings.Repeat("x", 400), 5, nil, strings.Repeat("x", 300)},
		{"read error", "unavailable", 5, errors.New("read failed"), ""},
		{"no lines", "first", 0, nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hcRun = func(args ...string) ([]byte, error) {
				if !reflect.DeepEqual(args, []string{"read", "t9"}) {
					t.Fatalf("read args %v", args)
				}
				return []byte(tt.screen), tt.err
			}
			if got := appTerminalTail("t9", tt.n); got != tt.want {
				t.Fatalf("tail=%q want %q", got, tt.want)
			}
		})
	}
}
func TestHCRawEnvironment(t *testing.T) {
	appTest(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "hc")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"${HC_TERMINAL_ID+x}\" = x ]; then exit 7; fi\nprintf '%s' \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, op := range []string{"run", "send"} {
		out, err := muxCommand("hc", op)
		if err != nil || string(out) != op {
			t.Fatalf("%s inherited identity: %s %v", op, out, err)
		}
	}
	if got := withoutHCTerminal([]string{"A=1", "HC_TERMINAL_ID=x", "HC_TERMINAL_SOCKET=s"}); !reflect.DeepEqual(got, []string{"A=1", "HC_TERMINAL_SOCKET=s"}) {
		t.Fatalf("env=%v", got)
	}
}
func TestAppReadinessAndTrust(t *testing.T) {
	for _, tt := range []struct {
		name, state, kind, text string
		trusted                 bool
		keys                    []string
	}{
		{"ready", "idle", "", "", true, nil},
		{"permission", "blocked", "permission", "Do you trust the files in this directory?", true, []string{"enter"}},
		{"claude choice", "blocked", "permission", "Do you trust the files in this directory? " + claudeTrustChoice, true, []string{"down", "enter"}},
		{"text trust", "idle", "", "Do you trust the files in this directory?", true, []string{"enter"}},
		{"outside cwd", "blocked", "permission", "", false, nil},
		{"outside cwd text", "idle", "", "Do you trust the files in this directory?", false, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			appTest(t)
			cwd, _ := os.Getwd()
			if !tt.trusted {
				cwd = t.TempDir()
			}
			accepted := false
			var keys []string
			hcRun = func(args ...string) ([]byte, error) {
				switch args[0] {
				case "get":
					state, kind := tt.state, tt.kind
					if accepted {
						state, kind = "idle", ""
					}
					return json.Marshal(hcTerminal{Address: "tw", State: "live", Status: &hcStatus{State: state, Kind: kind}})
				case "read":
					if accepted {
						return nil, nil
					}
					return []byte(tt.text), nil
				case "keys":
					keys = append(keys, args[2:]...)
					if slices.Contains(args[2:], "enter") {
						accepted = true
					}
					return nil, nil
				}
				return nil, fmt.Errorf("unexpected %v", args)
			}
			err := appWaitReady("tw", cwd)
			if !tt.trusted {
				if err == nil {
					t.Fatal("untrusted permission accepted")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(keys, tt.keys) {
				t.Fatalf("keys=%v want %v", keys, tt.keys)
			}
		})
	}
}
func TestAppCloseAndLookupErrors(t *testing.T) {
	for _, mode := range []string{"close", "lookup error", "inventory error", "already gone"} {
		t.Run(mode, func(t *testing.T) {
			appTest(t)
			killed, removed := false, false
			hcRPC = func(method string, params any) (json.RawMessage, error) {
				return json.RawMessage(`{"value":{"spaces":[]}}`), nil
			}
			hcRun = func(args ...string) ([]byte, error) {
				switch args[0] {
				case "get":
					if mode == "lookup error" {
						return nil, errors.New("server unavailable")
					}
					if mode == "already gone" {
						return nil, errHCNotFound
					}
					state := "live"
					if killed {
						state = "finished"
					}
					return json.Marshal(hcTerminal{Address: "tw", State: state, Labels: map[string]string{"orch.run": "run", "orch.worker": "worker"}})
				case "kill":
					killed = true
				case "rm":
					if !killed {
						t.Fatal("rm before finished")
					}
					removed = true
				default:
					t.Fatalf("unexpected %v", args)
				}
				return nil, nil
			}
			appRun = func(args ...string) ([]byte, error) {
				if mode == "inventory error" {
					return nil, errors.New("app unavailable")
				}
				return appLS(""), nil
			}
			if mode == "lookup error" {
				exists, err := paneExists("tw")
				if err == nil || exists {
					t.Fatal("lookup error treated as absent")
				}
			}
			err := appClose("tw", appIdentity{"run", "worker"})
			if mode == "lookup error" || mode == "inventory error" {
				if err == nil {
					t.Fatal("expected close error")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "close" && (!killed || !removed) {
				t.Fatalf("kill=%v rm=%v", killed, removed)
			}
		})
	}
}
func TestRunBackendMismatch(t *testing.T) {
	appTest(t)
	for _, b := range []string{"", "herdr", "app"} {
		r := &Run{ID: "r", Backend: b, HeadPane: "th"}
		err := ownRun(r)
		if b == "app" {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "backend") {
			t.Fatalf("missing mismatch: %v", err)
		}
	}
}
func TestAppAgentIO(t *testing.T) {
	appTest(t)
	r := &Run{ID: "r", Backend: "app", HeadPane: "th", Status: "active", Workers: []*Worker{{Name: "w", Pane: "pw", Terminal: "tw", Status: "spawned"}}}
	if err := os.MkdirAll(r.dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	appRun = func(...string) ([]byte, error) { return appLS(`{"terminal":"tw","pane":"pw"}`), nil }
	hcRun = func(args ...string) ([]byte, error) {
		if len(args) < 2 || args[1] != "tw" {
			t.Fatalf("wrong address %v", args)
		}
		switch args[0] {
		case "get":
			return []byte(`{"state":"live","status":{"state":"working"},"pid":123}`), nil
		case "read":
			return []byte("transcript"), nil
		case "keys":
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected %v", args)
	}
	info, err := agentGet("w")
	if err != nil || info.Pane != "pw" || info.Status != "working" {
		t.Fatalf("info=%v %v", info, err)
	}
	text, err := agentRead("w", 40)
	if err != nil || text != "transcript" {
		t.Fatalf("read %s %v", text, err)
	}
	if _, err = muxKeys("w", "enter"); err != nil {
		t.Fatal(err)
	}
	if pid, err := paneShellPID("pw"); err != nil || pid != 123 {
		t.Fatalf("pid %d %v", pid, err)
	}
}

// Exercise the actual spawn/close flow, including row capacity, persistent overflow
// sequence, pane/address ownership, and live document normalization.
func TestAppSpawnOverflowAndClose(t *testing.T) {
	appTest(t)
	t.Setenv("ORCH_ROLE", "")
	t.Setenv("ORCH_RUN", "")
	t.Setenv("ORCH_MAX_ROWS", "1")
	oldReaper := startReaper
	t.Cleanup(func() { startReaper = oldReaper })
	startReaper = func(string) error { return nil }
	type term struct{ address, pane, tab, state string }
	terms := []*term{{"th", "ph", "tab", "live"}}
	names := map[string]any{"tab": "Head"}
	roots := map[string]any{"tab": layoutNode(`{"type":"row","id":"tab","children":[` + appHeadBlock + `]}`)}
	rebuild := func(tab string) {
		children := []any{}
		for _, p := range terms {
			if p.tab == tab && p.state != "removed" {
				children = append(children, map[string]any{"type": "block", "id": p.pane, "content": map[string]any{"terminal": p.address}})
			}
		}
		if len(children) == 0 {
			delete(roots, tab)
		} else {
			roots[tab] = map[string]any{"type": "row", "id": tab, "children": children}
		}
	}
	appRun = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "ls":
			tabs := map[string][]appTerminal{}
			for _, p := range terms {
				if p.state != "removed" {
					tabs[p.tab] = append(tabs[p.tab], appTerminal{Terminal: p.address, Pane: p.pane})
				}
			}
			ls := appInventory{Spaces: []appSpace{{ID: "s"}}}
			for id, ps := range tabs {
				ls.Spaces[0].Tabs = append(ls.Spaces[0].Tabs, appTab{ID: id, Terminals: ps})
			}
			return json.Marshal(ls)
		case "do":
			if args[1] != "tab.rename" {
				t.Fatalf("unexpected app command %v", args)
			}
			return nil, fmt.Errorf("rename failed")
		}
		return nil, fmt.Errorf("unexpected app %v", args)
	}
	version := 1
	hcRPC = func(method string, params any) (json.RawMessage, error) {
		if method == "doc.get" {
			tabs := []any{}
			for id, root := range roots {
				tabs = append(tabs, map[string]any{"id": id, "root": root, "name": names[id]})
			}
			return json.Marshal(map[string]any{"version": version, "value": map[string]any{"spaces": []any{map[string]any{"id": "s", "tabs": tabs}}}})
		}
		if method != "doc.set" {
			t.Fatal(method)
		}
		p := params.(map[string]any)
		if p["version"].(int) != version {
			t.Fatal("stale write")
		}
		for id := range roots {
			tab, err := docTab(p["value"].(map[string]any), "s", id)
			if err != nil {
				t.Fatal(err)
			}
			roots[id] = tab["root"]
			names[id] = tab["name"]
		}
		version++
		return json.Marshal(map[string]int{"version": version})
	}
	owners := map[string]map[string]string{}
	hcRun = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "run":
			if !slices.Contains(args, "--no-daemon") {
				t.Fatalf("Codex daemon enabled: %v", args)
			}
			tab := ""
			for _, a := range args {
				if strings.HasPrefix(a, "herdr.tab=") {
					tab = strings.TrimPrefix(a, "herdr.tab=")
				}
			}
			address := fmt.Sprintf("tw%d", len(terms))
			labels := map[string]string{}
			for _, a := range args {
				for _, key := range []string{"orch.run", "orch.worker"} {
					if strings.HasPrefix(a, key+"=") {
						labels[key] = strings.TrimPrefix(a, key+"=")
					}
				}
			}
			owners[address] = labels
			terms = append(terms, &term{address, "p" + address, tab, "live"})
			rebuild(tab)
			return json.Marshal(hcTerminal{Address: address, State: "live"})
		case "get":
			for _, p := range terms {
				if p.address == args[1] && p.state != "removed" {
					return json.Marshal(hcTerminal{Address: p.address, State: p.state, Rows: 20, Cols: 120, Status: &hcStatus{State: "idle"}, Labels: owners[p.address]})
				}
			}
			return nil, errHCNotFound
		case "read":
			return nil, nil
		case "kill", "rm":
			for _, p := range terms {
				if p.address == args[1] {
					if p.address == "th" {
						t.Fatal("closed Head")
					}
					if args[0] == "kill" {
						p.state = "finished"
					} else {
						if p.state != "finished" {
							t.Fatal("removed live terminal")
						}
						p.state = "removed"
						rebuild(p.tab)
					}
					return nil, nil
				}
			}
		}
		return nil, fmt.Errorf("unexpected hc %v", args)
	}
	r, err := createRun("app integration", "light", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var workers []*Worker
	for i := 0; i < 3; i++ {
		w, err := spawn(spawnOpts{Run: r.ID, Name: fmt.Sprintf("w%d", i), Key: "codex-high-l", Role: roleProduce, Account: "same"})
		if err != nil {
			t.Fatal(err)
		}
		if w.Terminal == "" || w.Pane == w.Terminal {
			t.Fatalf("address/pane lost: %+v", w)
		}
		workers = append(workers, w)
	}
	if workers[0].Tab != "tab" || workers[1].Tab == "tab" || workers[1].Tab == workers[2].Tab {
		t.Fatalf("overflow tabs: %s %s %s", workers[0].Tab, workers[1].Tab, workers[2].Tab)
	}
	saved, err := loadRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, w := range workers[1:] {
		want := fmt.Sprintf("orch %s %d", r.ID, i+1)
		if saved.OverflowNames[w.Tab] != want || names[w.Tab] != want {
			t.Fatalf("overflow name: saved=%q document=%v want=%q", saved.OverflowNames[w.Tab], names[w.Tab], want)
		}
	}
	if names["tab"] != "Head" {
		t.Fatalf("head name changed: %v", names["tab"])
	}
	if saved.OverflowSeq != 2 {
		t.Fatalf("overflow sequence=%d", saved.OverflowSeq)
	}
	for _, w := range workers {
		if err := closeWorker(r.ID, w.Name); err != nil {
			t.Fatal(err)
		}
	}
	saved, err = loadRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.liveWorkers()) != 0 || len(saved.OverflowTabs) != 0 {
		t.Fatalf("ownership leaked: %+v", saved)
	}
	if len(roots) != 1 {
		t.Fatalf("overflow document tabs remain: %v", roots)
	}
}

func TestHCNotFoundExitStatus(t *testing.T) {
	appTest(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hc"), []byte("#!/bin/sh\nprintf '%s' '{\"error\":{\"kind\":\"not_found\"}}' >&2\nexit \"$HC_STUB_CODE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	hcRun = func(args ...string) ([]byte, error) { return muxCommand("hc", args...) }
	for _, code := range []string{"3", "1"} {
		t.Setenv("HC_STUB_CODE", code)
		exists, err := paneExists("tw")
		if exists {
			t.Fatal("missing terminal exists")
		}
		if code == "3" && err != nil {
			t.Fatal(err)
		}
		if code == "1" && err == nil {
			t.Fatal("non-not-found exit was treated as absent")
		}
	}
}

func TestAppCleanupEmptyTabsProtectsForeignBlocks(t *testing.T) {
	for _, mode := range []string{"empty", "terminal", "mod", "lookup error"} {
		t.Run(mode, func(t *testing.T) {
			appTest(t)
			closed := false
			appRun = func(args ...string) ([]byte, error) {
				if args[0] == "do" {
					if mode != "empty" {
						t.Fatal("closed foreign tab")
					}
					if !reflect.DeepEqual(args, []string{"do", "tab.close", "--id", "overflow", "--confirmed", "true"}) {
						t.Fatalf("args=%v", args)
					}
					closed = true
					return nil, nil
				}
				if mode == "lookup error" {
					return nil, errors.New("lookup failed")
				}
				if closed {
					return appLS(""), nil
				}
				terminals := []appTerminal{}
				if mode == "terminal" {
					terminals = append(terminals, appTerminal{Terminal: "foreign", Pane: "foreign-pane"})
				}
				return json.Marshal(appInventory{Spaces: []appSpace{{ID: "s", Tabs: []appTab{{ID: "overflow", Terminals: terminals}}}}})
			}
			hcRPC = func(method string, _ any) (json.RawMessage, error) {
				if method != "doc.get" {
					t.Fatal("wrote document during cleanup")
				}
				root := `{"type":"row","id":"overflow","children":[]}`
				if mode == "mod" {
					root = `{"type":"row","id":"overflow","children":[{"type":"block","id":"foreign","content":{"mod":"editor"}}]}`
				}
				return []byte(`{"version":1,"value":{"spaces":[{"id":"s","tabs":[{"id":"overflow","root":` + root + `}]}]}}`), nil
			}
			r := &Run{Backend: "app", OverflowTabs: []string{"overflow"}}
			err := appCleanupTabs(r)
			if mode == "empty" {
				if err != nil || !closed || len(r.OverflowTabs) > 0 {
					t.Fatalf("cleanup err=%v closed=%v tabs=%v", err, closed, r.OverflowTabs)
				}
			} else if err == nil || closed || len(r.OverflowTabs) != 1 {
				t.Fatalf("foreign or lookup error lost ownership: %v %v", r, err)
			}
		})
	}
}

func TestAppCloseWaitsForDocument(t *testing.T) {
	for _, tc := range []struct {
		name     string
		docError bool
	}{{"lingering leaf", false}, {"document error", true}} {
		t.Run(tc.name, func(t *testing.T) {
			appTest(t)
			hcRun = func(args ...string) ([]byte, error) { return nil, errHCNotFound }
			appRun = func(args ...string) ([]byte, error) { return appLS(`{"terminal":"th","pane":"ph"}`), nil }
			reads := 0
			hcRPC = func(method string, params any) (json.RawMessage, error) {
				reads++
				if tc.docError {
					return nil, fmt.Errorf("document unavailable")
				}
				root := appHeadBlock
				if reads == 1 {
					root = appWorkerOne
				}
				return json.RawMessage(`{"value":{"spaces":[{"tabs":[{"root":` + root + `}]}]}}`), nil
			}
			err := appClose("t1", appIdentity{"run", "worker"})
			if tc.docError {
				if err == nil || !strings.Contains(err.Error(), "document unavailable") {
					t.Fatalf("error: %v", err)
				}
				return
			}
			if err != nil || reads != 2 {
				t.Fatalf("error=%v reads=%d", err, reads)
			}
		})
	}
}

func TestAppCloseLayoutWarning(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failLayout bool
	}{{"normal layout", false}, {"layout failure", true}} {
		t.Run(tc.name, func(t *testing.T) {
			appTest(t)
			t.Setenv("ORCH_ROLE", "")
			t.Setenv("ORCH_RUN", "")
			appRun = func(args ...string) ([]byte, error) { return appLS(`{"terminal":"th","pane":"ph"}`), nil }
			hcRun = func(args ...string) ([]byte, error) { return nil, errHCNotFound }
			reads := 0
			hcRPC = func(method string, params any) (json.RawMessage, error) {
				reads++
				if tc.failLayout && reads > 1 {
					return nil, fmt.Errorf("layout unavailable")
				}
				return json.RawMessage(`{"value":{"spaces":[{"id":"s","tabs":[{"id":"tab","root":{"type":"row","id":"tab","children":[` + appHeadBlock + `]}}]}]}}`), nil
			}
			r, err := createRun("test", "light", "", "")
			if err != nil {
				t.Fatal(err)
			}
			r.Workers = []*Worker{{Name: "w", Pane: "pw", Terminal: "tw", Tab: "tab", Status: "running"}}
			if err = saveRun(r); err != nil {
				t.Fatal(err)
			}
			if err = closeWorker(r.ID, "w"); err != nil {
				t.Fatal(err)
			}
			r, err = loadRun(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			warning := false
			for _, e := range r.Events {
				warning = warning || strings.Contains(e.What, "warning: app layout normalization")
			}
			if warning != tc.failLayout || r.Workers[0].Pane != "" {
				t.Fatalf("warning=%v worker=%+v events=%v", warning, r.Workers[0], r.Events)
			}
		})
	}
}

func TestAppOwnedTabsKnownWorkers(t *testing.T) {
	appTest(t)
	appRun = func(args ...string) ([]byte, error) { return appLS(`{"terminal":"th","pane":"ph"}`), nil }
	r, err := createRun("test", "light", "", "")
	if err != nil {
		t.Fatal(err)
	}
	r.Workers = []*Worker{{Name: "live", Pane: "p1", Terminal: "t1", Tab: "tab", Status: "running"}, {Name: "closed", Terminal: "t2", Status: "closed"}, {Name: "lost", Pane: "p3", Terminal: "t3", Status: "lost"}}
	if err = saveRun(r); err != nil {
		t.Fatal(err)
	}
	tabs, known, err := appOwnedTabs("th")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tabs["tab"], []string{"t1"}) || !reflect.DeepEqual(known, map[string]appIdentity{"t1": {r.ID, "live"}, "t2": {r.ID, "closed"}, "t3": {r.ID, "lost"}}) {
		t.Fatalf("tabs=%v known=%v", tabs, known)
	}
}
