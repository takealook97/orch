package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

type HUDWorker struct {
	Terminal string `json:"terminal"`
	Pane     string `json:"pane"`
	Name     string `json:"name"`
	Key      string `json:"key"`
	Family   string `json:"family"`
	Role     string `json:"role"`
	Purpose  string `json:"purpose"`
	Status   string `json:"status"`
	Label    string `json:"label"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
}
type HUDRun struct {
	ID           string      `json:"id"`
	HeadTerminal string      `json:"head_terminal"`
	Workspace    string      `json:"workspace"`
	Tab          string      `json:"tab"`
	Mode         string      `json:"mode"`
	Workers      []HUDWorker `json:"workers"`
}
type HUDTerminal struct {
	Address string `json:"address"`
	Program string `json:"program"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}
type HUDPanes struct {
	At        time.Time     `json:"at"`
	Runs      []HUDRun      `json:"runs"`
	Terminals []HUDTerminal `json:"terminals"`
	Error     string        `json:"error,omitempty"`
}

func hudRoleLabel(role, purpose string) string {
	switch role {
	case roleRecon:
		if purpose == "web" {
			return "웹조사"
		}
		return "조사"
	case roleDesign:
		return "설계검토"
	case roleProduce:
		switch purpose {
		case "docs":
			return "문서"
		case "image":
			return "이미지"
		}
		return "구현"
	case roleIntegrate:
		return "통합"
	case roleMech:
		return "테스트"
	case roleCritical:
		return "리뷰"
	case roleNeutral:
		return "QA"
	case roleAdversarial:
		return "공격검증"
	}
	return role
}
func hudArgvModel(argv []string) (model, effort string) {
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		next := func() string {
			if i+1 < len(argv) {
				i++
				return argv[i]
			}
			return ""
		}
		switch {
		case arg == "--model" || arg == "-m":
			model = next()
		case strings.HasPrefix(arg, "--model="):
			model = strings.TrimPrefix(arg, "--model=")
		case arg == "--effort":
			effort = next()
		case strings.HasPrefix(arg, "--effort="):
			effort = strings.TrimPrefix(arg, "--effort=")
		case arg == "-c" || arg == "--config":
			value := next()
			if strings.HasPrefix(value, "model_reasoning_effort=") {
				effort = strings.Trim(strings.TrimPrefix(value, "model_reasoning_effort="), "\"'")
			}
		case strings.HasPrefix(arg, "-cmodel_reasoning_effort="):
			effort = strings.Trim(strings.TrimPrefix(arg, "-cmodel_reasoning_effort="), "\"'")
		}
	}
	return
}
func hudParseTerminals(raw []byte) ([]HUDTerminal, error) {
	var body struct {
		Terminals []struct {
			Address string   `json:"address"`
			Program string   `json:"program"`
			Agent   string   `json:"agent"`
			Argv    []string `json:"argv"`
		} `json:"terminals"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := []HUDTerminal{}
	for _, t := range body.Terminals {
		program := t.Program
		if program == "" {
			program = t.Agent
		}
		model, effort := hudArgvModel(t.Argv)
		out = append(out, HUDTerminal{t.Address, program, model, effort})
	}
	return out, nil
}
func hudPanes(ctx context.Context) HUDPanes {
	out := HUDPanes{At: time.Now(), Runs: []HUDRun{}, Terminals: []HUDTerminal{}}
	runs, err := allRuns()
	if err != nil {
		out.Error = "cannot read runs"
	}
	for _, r := range runs {
		if r.Status != "active" {
			continue
		}
		run := HUDRun{ID: r.ID, HeadTerminal: r.HeadPane, Workspace: r.Workspace, Tab: r.Tab, Mode: r.Mode, Workers: []HUDWorker{}}
		for _, w := range r.Workers {
			if w == nil {
				continue
			}
			worker := HUDWorker{Terminal: w.Terminal, Pane: w.Pane, Name: w.Name, Key: w.Key, Family: w.Family, Role: w.Role, Purpose: w.Purpose, Status: w.Status, Label: hudRoleLabel(w.Role, w.Purpose)}
			if m, err := modelByKey(w.Key); err == nil {
				worker.Model, _ = modelID(m)
				worker.Effort = m.Effort
			}
			run.Workers = append(run.Workers, worker)
		}
		out.Runs = append(out.Runs, run)
	}
	// One hc query covers non-orch panes too. Older hc versions omit argv, in
	// which case program still works and model/effort remain empty.
	raw, err := exec.CommandContext(ctx, "hc", "ls", "--json").Output()
	if err != nil {
		out.Error = strings.TrimSpace(out.Error + "; cannot list terminals")
		out.Error = strings.TrimPrefix(out.Error, "; ")
		return out
	}
	terminals, err := hudParseTerminals(raw)
	if err != nil {
		out.Error = "invalid terminal list"
	} else {
		out.Terminals = terminals
	}
	return out
}
