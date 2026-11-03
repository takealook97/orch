package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
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
	case "head":
		return "Head"
	case roleRecon:
		if purpose == "web" {
			return "Web"
		}
		return "Recon"
	case roleDesign:
		return "Design"
	case roleProduce:
		switch purpose {
		case "docs":
			return "Docs"
		case "image":
			return "Image"
		}
		return "Impl"
	case roleIntegrate:
		return "Integrate"
	case roleMech:
		return "Test"
	case roleCritical:
		return "Review"
	case roleNeutral:
		return "QA"
	case roleAdversarial:
		return "Adversarial"
	}
	return role
}

// hudShellSplit tokenizes launch scripts without executing expansions or commands.
func hudShellSplit(s string) []string {
	var out []string
	var token strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range s {
		if escaped {
			token.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				token.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				out = append(out, token.String())
				token.Reset()
				started = false
			}
		} else {
			token.WriteRune(r)
			started = true
		}
	}
	if escaped {
		token.WriteRune('\\')
	}
	if started {
		out = append(out, token.String())
	}
	return out
}
func hudArgvModel(argv []string) (model, effort string) {
	// hc get preserves the entire sh -c script as a single argument.
	tokens := append([]string(nil), argv...)
	for depth := 0; depth < 8; depth++ {
		expanded := []string{}
		changed := false
		for i, arg := range tokens {
			script := i >= 2 && tokens[i-1] == "-c" && (filepath.Base(tokens[i-2]) == "sh" || filepath.Base(tokens[i-2]) == "bash" || filepath.Base(tokens[i-2]) == "zsh")
			if script || (len(tokens) == 1 && strings.ContainsAny(arg, " \t")) || strings.Contains(arg, " --model") || strings.Contains(arg, " --effort") {
				parts := hudShellSplit(arg)
				if len(parts) > 1 {
					expanded = append(expanded, parts...)
					changed = true
					continue
				}
			}
			expanded = append(expanded, arg)
		}
		tokens = expanded
		if !changed {
			break
		}
	}
	config := func(value string) {
		key, value, ok := strings.Cut(value, "=")
		if !ok {
			return
		}
		value = strings.Trim(value, "\"'")
		if key == "model_reasoning_effort" {
			effort = value
		}
		if key == "model" {
			model = value
		}
	}
	for i := 0; i < len(tokens); i++ {
		arg := tokens[i]
		next := func() string {
			if i+1 < len(tokens) {
				i++
				return tokens[i]
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
			if i+1 < len(tokens) && strings.Contains(tokens[i+1], "=") && !strings.HasPrefix(tokens[i+1], "-") {
				config(next())
			}
		case strings.HasPrefix(arg, "--config="):
			config(strings.TrimPrefix(arg, "--config="))
		case strings.HasPrefix(arg, "-cmodel"):
			config(strings.TrimPrefix(arg, "-c"))
		}
	}
	return
}
func hudIsAgent(program string) bool {
	if program == "" {
		return false
	}
	switch filepath.Base(program) {
	case "", "sh", "bash", "zsh", "fish", "dash", "ksh":
		return false
	}
	return true
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
		program := t.Agent
		if program == "" {
			program = t.Program
		}
		if !hudIsAgent(program) || strings.Contains(t.Address, "/") {
			continue
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
	// ls supplies agent identity; get supplies launch argv.
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
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for i := range terminals {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-sem }()
				child, cancel := context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				raw, err := exec.CommandContext(child, "hc", "get", terminals[i].Address, "--json").Output()
				if err != nil {
					return
				}
				var detail struct {
					Argv []string `json:"argv"`
				}
				if json.Unmarshal(raw, &detail) == nil {
					out.Terminals[i].Model, out.Terminals[i].Effort = hudArgvModel(detail.Argv)
				}
			}(i)
		}
		wg.Wait()
	}
	return out
}
