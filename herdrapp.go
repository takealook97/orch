package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

func muxCommand(bin string, args ...string) ([]byte, error) {
	c := exec.Command(bin, args...)
	// Raw sends and independent runs must never inherit hc's sender/parent identity.
	c.Env = withoutHCTerminal(os.Environ())
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, stderr.String())
	}
	return out, nil
}
func withoutHCTerminal(env []string) []string {
	out := []string{}
	for _, e := range env {
		if !strings.HasPrefix(e, "HC_TERMINAL_ID=") {
			out = append(out, e)
		}
	}
	return out
}

var hcRun = func(args ...string) ([]byte, error) { return muxCommand("hc", args...) }
var appRun = func(args ...string) ([]byte, error) { return muxCommand("herdr-app", args...) }

// Each request uses a bounded connect session; document versions protect writes across sessions.
var hcRPC = func(method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "hc", "connect")
	c.Env = withoutHCTerminal(os.Environ())
	in, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err = c.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = in.Close(); _ = c.Process.Kill(); _ = c.Wait() }()
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
	if err = json.NewEncoder(in).Encode(req); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 16<<20)
	for scanner.Scan() {
		var r struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &r); err != nil {
			return nil, err
		}
		if r.ID != 1 {
			continue
		}
		if len(r.Error) > 0 && string(r.Error) != "null" {
			return nil, fmt.Errorf("hc RPC: %s", r.Error)
		}
		return r.Result, nil
	}
	return nil, fmt.Errorf("hc RPC ended without response: %v", scanner.Err())
}

type appTerminal struct {
	Terminal string `json:"terminal"`
	Pane     string `json:"pane"`
	Status   string `json:"status"`
	App      string `json:"app"`
}
type appTab struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Terminals []appTerminal `json:"terminals"`
}
type appSpace struct {
	ID   string   `json:"id"`
	Tabs []appTab `json:"tabs"`
}
type appInventory struct {
	Spaces []appSpace `json:"spaces"`
}

func appList() (appInventory, error) {
	var ls appInventory
	out, err := appRun("ls")
	if err == nil {
		err = json.Unmarshal(out, &ls)
	}
	return ls, err
}

type hcStatus struct {
	App   string `json:"app"`
	State string `json:"state"`
	Kind  string `json:"kind"`
	Msg   string `json:"msg"`
}
type hcTerminal struct {
	Address string            `json:"address"`
	State   string            `json:"state"`
	PID     int               `json:"pid"`
	Rows    int               `json:"rows"`
	Cols    int               `json:"cols"`
	Status  *hcStatus         `json:"status"`
	Labels  map[string]string `json:"labels"`
}

type appIdentity struct{ run, worker string }

func appOwnedLabels(t hcTerminal, owner appIdentity) bool {
	return owner.run != "" && owner.worker != "" && t.Labels["orch.run"] == owner.run && t.Labels["orch.worker"] == owner.worker
}

func appCheckIdentity(address string, owner appIdentity) (hcTerminal, error) {
	t, err := hcGet(address)
	if errors.Is(err, errHCNotFound) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	if t.State != "finished" && !appOwnedLabels(t, owner) {
		return t, fmt.Errorf("I6: terminal %s has foreign or missing ownership labels", address)
	}
	return t, nil
}

func appRemoveFinished(address string, owner appIdentity) {
	t, err := appCheckIdentity(address, owner)
	if err == nil && t.State == "finished" && appOwnedLabels(t, owner) {
		_, _ = hcRun("rm", address)
	}
}

var errHCNotFound = errors.New("hc terminal not found")

func hcGet(id string) (hcTerminal, error) {
	var t hcTerminal
	out, err := hcRun("get", id, "--json")
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 3 && strings.Contains(err.Error(), `"not_found"`) {
			return t, errHCNotFound
		}
		return t, err
	}
	err = json.Unmarshal(out, &t)
	return t, err
}

// checkRunBackend refuses to act on another backend's panes. A run without a Head pane
// (an ad-hoc task run) owns no panes and works under either backend.
func checkRunBackend(r *Run) error {
	b := currentBackend()
	if r.HeadPane == "" {
		return nil
	}
	if runBackend(r) != b {
		return fmt.Errorf("run %s uses %s backend; current backend is %s", r.ID, runBackend(r), b)
	}
	return nil
}
func appAddress(id string) string {
	if id == headID() {
		return id
	}
	runs, _ := allRuns()
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		if runBackend(r) != "app" {
			continue
		}
		for i := len(r.Workers) - 1; i >= 0; i-- {
			w := r.Workers[i]
			if (w.Pane == id || w.Name == id && r.HeadPane == headID() && w.Pane != "") && w.Terminal != "" {
				return w.Terminal
			}
		}
	}
	return id
}
func appPane(id string) (string, string, string, error) {
	ls, err := appList()
	if err != nil {
		return "", "", "", err
	}
	for _, s := range ls.Spaces {
		for _, t := range s.Tabs {
			for _, p := range t.Terminals {
				if p.Terminal == id || p.Pane == id {
					return s.ID, t.ID, p.Pane, nil
				}
			}
		}
	}
	return "", "", "", fmt.Errorf("terminal %s absent from herdr-app", id)
}
func appPanes(space string) ([]PaneInfo, error) {
	ls, err := appList()
	if err != nil {
		return nil, err
	}
	var out []PaneInfo
	for _, s := range ls.Spaces {
		if space != "" && space != s.ID {
			continue
		}
		for _, t := range s.Tabs {
			for _, p := range t.Terminals {
				out = append(out, PaneInfo{ID: p.Pane, Tab: t.ID, AgentStatus: p.Status})
			}
		}
	}
	return out, nil
}
func appExists(id string) (bool, error) {
	t, err := hcGet(appAddress(id))
	if errors.Is(err, errHCNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return t.State == "live", nil
}
func appClose(id string, owners ...appIdentity) error {
	owner := appIdentity{}
	if len(owners) > 0 {
		owner = owners[0]
	}
	address := id
	t, err := appCheckIdentity(address, owner)
	if err != nil {
		return err
	}
	if t.State == "" {
		err = errHCNotFound
	}
	if err == nil && t.State != "finished" {
		if _, err = hcRun("kill", address); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		t, err = hcGet(address)
		if errors.Is(err, errHCNotFound) || err == nil && t.State == "finished" {
			break
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("terminal %s did not finish", address)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err == nil && appOwnedLabels(t, owner) {
		if _, err = hcRun("rm", address); err != nil {
			return err
		}
	}
	for {
		ls, err := appList()
		if err != nil {
			return fmt.Errorf("confirm terminal removal: %w", err)
		}
		found := false
		for _, s := range ls.Spaces {
			for _, t := range s.Tabs {
				for _, p := range t.Terminals {
					found = found || p.Terminal == address
				}
			}
		}
		if !found {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("terminal %s still in herdr-app", address)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for {
		d, err := appDocGet()
		if err != nil {
			return fmt.Errorf("confirm document removal: %w", err)
		}
		found := false
		spaces, _ := d.Value["spaces"].([]any)
		for _, sv := range spaces {
			space, _ := sv.(map[string]any)
			tabs, _ := space["tabs"].([]any)
			for _, tv := range tabs {
				tab, _ := tv.(map[string]any)
				leaves, err := appLeaves(tab["root"])
				if err != nil {
					continue
				}
				for _, leaf := range leaves {
					found = found || leafTerminal(leaf) == address
				}
			}
		}
		if !found {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("terminal %s still in app document", address)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
func appAgentGet(name string) (AgentInfo, error) {
	address := appAddress(name)
	t, err := hcGet(address)
	if err != nil {
		return AgentInfo{}, err
	}
	if t.State != "live" || t.Status == nil {
		return AgentInfo{}, fmt.Errorf("terminal %s is not a ready live agent", address)
	}
	_, _, pane, err := appPane(address)
	return AgentInfo{Name: name, Pane: pane, Status: t.Status.State}, err
}
func muxKeys(name string, keys ...string) ([]byte, error) {
	if backend() == "app" {
		return hcRun(append([]string{"keys", appAddress(name)}, keys...)...)
	}
	return herdrRun(append([]string{"agent", "send-keys", name}, keys...)...)
}
func appTrust(name, cwd string) bool {
	if !trustable(cwd) {
		return false
	}
	text, _ := agentRead(name, 40)
	if !trustDialog.MatchString(text) {
		return false
	}
	if strings.Contains(text, claudeTrustChoice) {
		if _, err := muxKeys(name, "down"); err != nil {
			return false
		}
		time.Sleep(timings.KeyGap)
	}
	_, err := muxKeys(name, "enter")
	return err == nil
}
func appWaitReady(address, cwd string) error {
	deadline := time.Now().Add(timings.ReadyTimeout)
	for {
		t, err := hcGet(address)
		if err == nil && t.State == "live" {
			if !paneShowsTrust(address) {
				if t.Status != nil {
					return nil
				}
			} else if appTrust(address, cwd) {
				time.Sleep(timings.ReadyPoll)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("terminal %s not ready (login or folder trust may be blocking): %v", address, err)
		}
		time.Sleep(timings.ReadyPoll)
	}
}
func appLaunch(r *Run, tab, cwd, name string, env, argv []string) (string, string, error) {
	args := []string{"run", "--bg", "--json", "--no-parent", "--no-notify", "--cwd", cwd, "--label", "herdr.space=" + r.Workspace, "--label", "herdr.tab=" + tab, "--label", "orch.run=" + r.ID, "--label", "orch.worker=" + name, "--", "env"}
	args = append(args, env...)
	args = append(args, argv...)
	out, err := hcRun(args...)
	if err != nil {
		return "", "", err
	}
	var t hcTerminal
	if err = json.Unmarshal(out, &t); err != nil {
		return "", "", err
	}
	if t.Address == "" {
		return "", "", fmt.Errorf("hc run returned no address")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, pane, err := appPane(t.Address)
		if err == nil {
			return t.Address, pane, nil
		}
		if time.Now().After(deadline) {
			_ = appClose(t.Address, appIdentity{r.ID, name})
			return "", "", fmt.Errorf("app pane lookup: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
func appSelectTab(r *Run, l Layout) (string, error) {
	if err := workerCapacity(r); err != nil {
		return "", err
	}
	counts := map[string]int{}
	runs, err := allRuns()
	if err != nil {
		return "", err
	}
	for _, run := range runs {
		if run.HeadPane == r.HeadPane && runBackend(run) == "app" {
			for _, w := range run.liveWorkers() {
				counts[w.Tab]++
			}
		}
	}
	if counts[r.Tab] < rowCap(l) {
		return r.Tab, nil
	}
	for _, tab := range r.OverflowTabs {
		if counts[tab] < rowCap(l) {
			d, err := appDocGet()
			if err != nil {
				return "", err
			}
			t, err := docTab(d.Value, r.Workspace, tab)
			if err != nil {
				continue
			}
			owned, retired, err := appOwnedTabs(r.HeadPane)
			if err != nil {
				return "", err
			}
			if _, err = canonicalAppRoot(t["root"], tab, "", owned[tab], retired); err != nil {
				return "", err
			}
			return tab, nil
		}
	}
	if err := withRun(r.ID, func(current *Run) error {
		current.OverflowSeq = max(current.OverflowSeq, len(current.OverflowTabs)) + 1
		r.OverflowSeq = current.OverflowSeq
		tab := fmt.Sprintf("tborch%s%d", strings.ReplaceAll(r.ID, "-", ""), r.OverflowSeq)
		if current.OverflowNames == nil {
			current.OverflowNames = map[string]string{}
		}
		current.OverflowNames[tab] = fmt.Sprintf("orch %s %d", r.ID, r.OverflowSeq)
		r.OverflowNames = current.OverflowNames
		return nil
	}); err != nil {
		return "", err
	}
	return fmt.Sprintf("tborch%s%d", strings.ReplaceAll(r.ID, "-", ""), r.OverflowSeq), nil
}
func appCleanupTabs(r *Run) error {
	ls, err := appList()
	if err != nil {
		return err
	}
	kept := []string{}
	for _, id := range r.OverflowTabs {
		if len(overflowPanes(r, id)) > 0 {
			kept = append(kept, id)
			continue
		}
		for _, s := range ls.Spaces {
			for _, t := range s.Tabs {
				if t.ID != id {
					continue
				}
				if len(t.Terminals) > 0 {
					return fmt.Errorf("I6: overflow tab %s contains foreign terminals", id)
				}
				d, err := appDocGet()
				if err != nil {
					return err
				}
				dt, err := docTab(d.Value, s.ID, id)
				if err != nil {
					return err
				}
				leaves, err := appLeaves(dt["root"])
				if err != nil {
					return err
				}
				if len(leaves) > 0 {
					return fmt.Errorf("I6: overflow tab %s contains foreign blocks", id)
				}
				if _, err = appRun("do", "tab.close", "--id", id, "--confirmed", "true"); err != nil {
					return err
				}
				check, err := appList()
				if err != nil {
					return err
				}
				for _, ss := range check.Spaces {
					for _, tt := range ss.Tabs {
						if tt.ID == id {
							return fmt.Errorf("overflow tab %s did not close", id)
						}
					}
				}
			}
		}
	}
	for _, id := range r.OverflowTabs {
		if !slices.Contains(kept, id) {
			delete(r.OverflowNames, id)
		}
	}
	r.OverflowTabs = kept
	return nil
}

func workerAgent(w *Worker) string {
	if backend() == "app" {
		return w.Terminal
	}
	return w.Name
}
