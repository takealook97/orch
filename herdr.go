package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// herdrRun is swappable in tests.
var herdrRun = runHerdrControl

func runHerdrControl(args ...string) ([]byte, error) {
	return boundedControl("herdr", args, os.Environ(), controlDeadline("herdr", args))
}

func herdrJSON(v any, args ...string) error {
	out, err := herdrRun(args...)
	if err != nil {
		return err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("cannot parse Herdr response: %w", err)
	}
	if v == nil {
		return nil
	}
	return json.Unmarshal(env.Result, v)
}

type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type LayoutPane struct {
	ID   string `json:"pane_id"`
	Rect Rect   `json:"rect"`
}

type LayoutSplit struct {
	ID        string  `json:"id"`
	Direction string  `json:"direction"`
	Ratio     float64 `json:"ratio"`
	Rect      Rect    `json:"rect"`
}

type Layout struct {
	Area   Rect          `json:"area"`
	Panes  []LayoutPane  `json:"panes"`
	Splits []LayoutSplit `json:"splits"`
	Zoomed bool          `json:"zoomed"`
}

func getLayout(pane string) (Layout, error) {
	if backend() == "app" {
		return appGetLayout(pane)
	}
	var res struct {
		Layout Layout `json:"layout"`
	}
	err := herdrJSON(&res, "pane", "layout", "--pane", pane)
	return res.Layout, err
}

func splitPane(target, direction string, ratio float64, cwd string, env []string) (string, error) {
	args := []string{"pane", "split", "--pane", target, "--direction", direction, "--cwd", cwd, "--no-focus"}
	if ratio > 0 {
		args = append(args, "--ratio", fmt.Sprintf("%.4f", ratio))
	}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	var res struct {
		Pane struct {
			ID string `json:"pane_id"`
		} `json:"pane"`
	}
	if err := herdrJSON(&res, args...); err != nil {
		return "", err
	}
	if res.Pane.ID == "" {
		return "", errors.New("Herdr split response has no pane_id")
	}
	return res.Pane.ID, nil
}

type PaneInfo struct {
	ID          string `json:"pane_id"`
	Tab         string `json:"tab_id"`
	AgentStatus string `json:"agent_status"`
}

func listPanes(workspace string) ([]PaneInfo, error) {
	if backend() == "app" {
		return appPanes(workspace)
	}
	args := []string{"pane", "list"}
	if workspace != "" {
		args = append(args, "--workspace", workspace)
	}
	var res struct {
		Panes []PaneInfo `json:"panes"`
	}
	err := herdrJSON(&res, args...)
	return res.Panes, err
}

// paneExists reports whether a pane is live. A failed lookup is an error, never "absent".
func paneExists(id string) (bool, error) {
	if backend() == "app" {
		return appExists(id)
	}
	panes, err := listPanes("")
	if err != nil {
		return false, err
	}
	for _, p := range panes {
		if p.ID == id {
			return true, nil
		}
	}
	return false, nil
}

// closePane closes a pane and confirms its removal from the live pane list.
func closePane(id string) error {
	if backend() == "app" {
		return appClose(id)
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		_, closeErr := herdrRun("pane", "close", id)
		exists, err := paneExists(id)
		if err != nil {
			lastErr = fmt.Errorf("could not confirm removal of pane %s: %w", id, err)
			continue
		}
		if !exists {
			return nil
		}
		lastErr = fmt.Errorf("pane %s did not close (%v)", id, closeErr)
	}
	return lastErr
}

type AgentInfo struct {
	Name   string `json:"name"`
	Status string `json:"agent_status"`
	Pane   string `json:"pane_id"`
}

func agentGet(name string) (AgentInfo, error) {
	if backend() == "app" {
		return appAgentGet(name)
	}
	var raw map[string]any
	if err := herdrJSON(&raw, "agent", "get", name); err != nil {
		return AgentInfo{}, err
	}
	info := AgentInfo{Name: name}
	// The agent record may be nested; search for the fields we need.
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k, val := range t {
				if s, ok := val.(string); ok {
					switch k {
					case "agent_status", "status":
						if info.Status == "" {
							info.Status = s
						}
					case "pane_id":
						if info.Pane == "" {
							info.Pane = s
						}
					}
				}
				walk(val)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(raw)
	return info, nil
}

func agentRead(name string, lines int) (string, error) {
	if backend() == "app" {
		out, err := hcRun("read", appAddress(name), "--lines", fmt.Sprint(lines))
		return string(out), err
	}
	out, err := herdrRun("agent", "read", name, "--source", "recent-unwrapped", "--lines", fmt.Sprint(lines))
	if err != nil {
		return "", err
	}
	var env struct {
		Result map[string]any `json:"result"`
	}
	if json.Unmarshal(out, &env) == nil {
		for _, key := range []string{"text", "content", "output"} {
			if s, ok := env.Result[key].(string); ok {
				return s, nil
			}
		}
		for _, v := range env.Result {
			if m, ok := v.(map[string]any); ok {
				for _, key := range []string{"text", "content", "output"} {
					if s, ok := m[key].(string); ok {
						return s, nil
					}
				}
			}
		}
	}
	return string(out), nil
}

// paneShellPID returns the shell PID Herdr reports for a pane.
func paneShellPID(pane string) (int, error) {
	if backend() == "app" {
		t, err := hcGet(appAddress(pane))
		if err != nil {
			return 0, err
		}
		if t.PID == 0 {
			return 0, fmt.Errorf("terminal %s pid unavailable", pane)
		}
		return t.PID, nil
	}
	var res struct {
		Info struct {
			ShellPID int `json:"shell_pid"`
		} `json:"process_info"`
	}
	if err := herdrJSON(&res, "pane", "process-info", "--pane", pane); err != nil {
		return 0, err
	}
	if res.Info.ShellPID == 0 {
		return 0, fmt.Errorf("pane %s shell_pid is unavailable", pane)
	}
	return res.Info.ShellPID, nil
}

// parentPID is swappable in tests.
var parentPID = func(pid int) (int, error) {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return 0, err
	}
	var ppid int
	_, err = fmt.Sscan(strings.TrimSpace(string(out)), &ppid)
	return ppid, err
}

// verifyCaller proves this process really runs inside pane: its shell must be an ancestor.
// HERDR_PANE_ID alone is only an environment variable and can be forged.
func verifyCaller(pane string) error {
	shell, err := paneShellPID(pane)
	if err != nil {
		return fmt.Errorf("Head identity check failed: %w", err)
	}
	pid := os.Getpid()
	for i := 0; i < 64 && pid > 1; i++ {
		if pid == shell {
			return nil
		}
		if pid, err = parentPID(pid); err != nil {
			return fmt.Errorf("Head identity check failed: %w", err)
		}
	}
	return fmt.Errorf("this process is not running in pane %s (HERDR_PANE_ID mismatch). You may manage only runs from your own pane", pane)
}
