package main

import "fmt"

// cliAdapter owns both invocation modes for one CLI. Adding a CLI flag belongs here.
type cliAdapter interface {
	interactive(Model, bool) ([]string, error)
	task(Model, string, string, string, string) ([]string, error)
	resume(Model, string, string, string, string, string) ([]string, error)
}
type codexAdapter struct{}
type claudeAdapter struct{}
type agyAdapter struct{}

// Shared flags are defined once for both invocation modes.
const codexBypassFlag = "--dangerously-bypass-approvals-and-sandbox"
const claudeSkipPermissionsFlag = "--dangerously-skip-permissions"
const agySkipPermissionsFlag = "--dangerously-skip-permissions"

func adapterFor(kind string) (cliAdapter, error) {
	switch kind {
	case "codex":
		return codexAdapter{}, nil
	case "claude":
		return claudeAdapter{}, nil
	case "agy":
		return agyAdapter{}, nil
	}
	return nil, fmt.Errorf("unknown CLI kind %s", kind)
}

// modelArgs selects the resolved model and effort for every CLI invocation.
func modelArgs(m Model) ([]string, error) {
	m, id, _, err := resolveModel(m)
	if err != nil {
		return nil, err
	}
	args := []string{"--model", id}
	switch {
	case m.Effort == "":
	case m.Kind == "codex":
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", m.Effort))
	default:
		args = append(args, "--effort", m.Effort)
	}
	return args, nil
}
func (codexAdapter) interactive(m Model, noDaemon bool) ([]string, error) {
	ma, err := modelArgs(m)
	if err != nil {
		return nil, err
	}
	args := []string{codexBypassFlag, "--no-alt-screen"}
	if noDaemon {
		args = append(args, "--no-daemon")
	}
	return append(args, ma...), nil
}
func (codexAdapter) task(m Model, kind, cwd, answer, prompt string) ([]string, error) {
	ma, err := modelArgs(m)
	if err != nil {
		return nil, err
	}
	a := append([]string{"exec"}, ma...)
	if kind == "ask" {
		a = append(a, "--sandbox", "read-only")
	} else {
		a = append(a, codexBypassFlag)
	}
	return append(a, "--skip-git-repo-check", "--json", "-C", cwd, "-o", answer, prompt), nil
}
func (claudeAdapter) interactive(m Model, noDaemon bool) ([]string, error) {
	ma, err := modelArgs(m)
	if err != nil {
		return nil, err
	}
	return append([]string{claudeSkipPermissionsFlag}, ma...), nil
}
func (claudeAdapter) task(m Model, kind, cwd, answer, prompt string) ([]string, error) {
	ma, err := modelArgs(m)
	if err != nil {
		return nil, err
	}
	a := append([]string{"-p"}, ma...)
	if kind == "ask" {
		// dontAsk denies anything not pre-approved, so the read-only tools are listed twice:
		// --tools makes them available, --allowedTools lets them run without a prompt.
		a = append(a, "--tools", "Read,Grep,Glob,WebSearch,WebFetch", "--allowedTools", "Read,Grep,Glob,WebSearch,WebFetch", "--permission-mode", "dontAsk")
	} else {
		a = append(a, "--permission-mode", "bypassPermissions")
	}
	// No MCP connectors and no skill listing: a one-off task needs neither, and together
	// they add a large prompt to every call that the cheaper key would pay for.
	return append(a, "--strict-mcp-config", "--disable-slash-commands", "--output-format", "json", prompt), nil
}
func (agyAdapter) interactive(m Model, noDaemon bool) ([]string, error) {
	ma, err := modelArgs(m)
	if err != nil {
		return nil, err
	}
	return append([]string{agySkipPermissionsFlag}, ma...), nil
}
func (agyAdapter) task(m Model, kind, cwd, answer, prompt string) ([]string, error) {
	ma, err := modelArgs(m)
	if err != nil {
		return nil, err
	}
	// agy's -p takes the prompt as its value (unlike claude's -p flag), so the
	// prompt must follow -p directly; anything else is read as the prompt.
	a := append([]string{}, ma...)
	if kind == "ask" {
		a = append(a, "--mode", "plan")
	} else {
		a = append(a, agySkipPermissionsFlag)
	}
	return append(a, "--output-format", "json", "-p", prompt), nil
}

func (codexAdapter) resume(m Model, kind, cwd, answer, sessionID, prompt string) ([]string, error) {
	ma, err := modelArgs(m)
	if err != nil {
		return nil, err
	}
	a := []string{"exec", "-C", cwd}
	if kind == "ask" {
		a = append(a, "--sandbox", "read-only")
	} else {
		a = append(a, codexBypassFlag)
	}
	a = append(a, "resume")
	a = append(a, ma...)
	return append(a, "--skip-git-repo-check", "--json", "-o", answer, sessionID, prompt), nil
}
func (claudeAdapter) resume(m Model, kind, cwd, answer, sessionID, prompt string) ([]string, error) {
	a, err := (claudeAdapter{}).task(m, kind, cwd, answer, prompt)
	if err != nil {
		return nil, err
	}
	return append(a[:len(a)-1], "--resume", sessionID, prompt), nil
}
func (agyAdapter) resume(m Model, kind, cwd, answer, sessionID, prompt string) ([]string, error) {
	return nil, fmt.Errorf("agy session resume is unsupported")
}
