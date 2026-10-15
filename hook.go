package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PreToolUse guard shared by Claude, Codex and agy.
//
//   worker (ORCH_ROLE=worker): no built-in subagents, no pane/agent creation, no orch delegation.
//   Head inside Herdr:       built-in subagents are replaced by orch; raw pane/agent creation too.
//   outside Herdr:           no opinion.
//
// A Head may run a raw Herdr command deliberately by prefixing ORCH_RAW_HERDR=1.

// Tool names may carry a namespace, with or without a separator
// (Codex reports collaborationspawn_agent; agy has invoke_subagent/manage_subagents).
var subagentTools = regexp.MustCompile(`(?i)(^([\w-]+(\.|__))*(agent|task|workflow)$|(spawn_agents?|manage_subagents|invoke_subagent|define_subagent|subagents?)$)`)

// Commands only count in command position (line start, after ; & | ( or $( ), optionally
// behind VAR=value prefixes or env/exec/command. Mentions inside prose or quoted text do not.
// Nested shells (bash -c '…', sh -c "…", eval '…') open a new command position. Backticks do
// not: markdown code spans in reports would trip it, and $( ) already covers substitution.
const cmdPos = `(?m)(^|[;&|(]|\$\(|\b(?:ba|z|da|k|fi)?sh\s+(?:-\w+\s+)*-\w*c\w*\s+['"]?|\beval\s+['"]?|\bxargs\s+(?:-\S+\s+)*)\s*((\w+=\S*|env(\s+-\S+(\s+\w+)?)*|exec|command|sudo|nohup|time)\s+)*`

var paneCreate = regexp.MustCompile(cmdPos + `(\S*/)?herdr\s+(pane\s+split|agent\s+start|tab\s+create|workspace\s+create|worktree\s+create)\b`)

var orchDelegate = regexp.MustCompile(cmdPos + `(\S*/)?orch\s+(spawn|prompt|run\s+new|fallback)\b`)

// A worker may not drive orch at all (close, resume, gc, run end included).
var orchAny = regexp.MustCompile(cmdPos + `(\S*/)?orch\s+[a-z]`)

var agentCLI = regexp.MustCompile(cmdPos + `(\S*/)?(codex\s+(exec|review|e)\b|claude\s+(-p|--print)\b|agy\s+(-p|--print)\b|gemini\s+-p\b)`)

type hookInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	ToolCall  *struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"toolCall"`
}

func (h hookInput) tool() string {
	if h.ToolName != "" {
		return h.ToolName
	}
	if h.ToolCall != nil {
		return h.ToolCall.Name
	}
	return ""
}

// commandText collects every string in the tool input so shell commands are found
// regardless of the CLI's argument shape (command, cmd, CommandLine, argv arrays).
func (h hookInput) commandText() string {
	raw := h.ToolInput
	if len(raw) == 0 && h.ToolCall != nil {
		raw = h.ToolCall.Args
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	var parts []string
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			// Some CLIs pass arguments as a JSON string; look inside it too.
			var inner any
			if strings.HasPrefix(strings.TrimSpace(t), "{") && json.Unmarshal([]byte(t), &inner) == nil {
				walk(inner)
				return
			}
			parts = append(parts, t)
		case []any:
			for _, y := range t {
				walk(y)
			}
		case map[string]any:
			for _, y := range t {
				walk(y)
			}
		}
	}
	walk(v)
	// One line per value: each value may be a command, and command position is per line.
	return strings.Join(parts, "\n")
}

// decide returns a deny reason, or "" to stay silent.
func decide(in hookInput, role string, inHerdr bool) string {
	tool := in.tool()
	cmd := in.commandText()
	// The shell joins quoted fragments (he''rdr, or''ch, \herdr); match the joined form too.
	cmd += "\n" + quoteJoiner.Replace(cmd)
	if role == "worker" {
		switch {
		case subagentTools.MatchString(tool):
			return "orch workers cannot delegate. Work within the contract scope and list any blockers in the result report."
		case paneCreate.MatchString(cmd) || orchDelegate.MatchString(cmd) || orchAny.MatchString(cmd):
			return "orch workers cannot create panes, agents, or orch delegation. List any blockers in the result report."
		case agentCLI.MatchString(cmd):
			return "orch workers cannot call another agent CLI (no delegation)."
		}
		return ""
	}
	if !inHerdr {
		return ""
	}
	if reason := goalScopeReason(in); reason != "" {
		return reason
	}
	if strings.Contains(cmd, "ORCH_RAW_HERDR=1") {
		return ""
	}
	switch {
	case subagentTools.MatchString(tool):
		return "Inside Herdr, use orch ask for one-off read-only tasks, orch do for one-off writing tasks, and orch spawn for long, parallel, or verification work."
	case headCreatesPane(in.commandText()):
		return "Create worker panes only with orch spawn (fixed model, layout, ORCH_ROLE injection). For a necessary non-delegation command pane, prefix the command with ORCH_RAW_HERDR=1."
	}
	return ""
}

// The Head's pane rule is a redirect, not a security boundary (ORCH_RAW_HERDR=1 bypasses it),
// so it skips two false positives without parsing quotes:
//   - an escaped separator (grep "a\|herdr pane split") is literal in every shell context
//     and starts no command;
//   - a help lookup (herdr pane split --help) creates nothing. Only the plain words before
//     the first separator, bracket, quote, redirection, comment or "--" count as its arguments.
//
// The worker rule above stays strict.
var helpFlag = regexp.MustCompile(`(^|\s)(--help|-h)(\s|$)`)

// A comment or an end-of-options marker ends the words herdr itself reads.
var argsEnd = regexp.MustCompile(`\s(#|--(\s|$))`)

// Inside a nested shell's double-quoted script a backslash before ; & | is literal, so
// escapes on such a line are left alone.
var nestedShell = regexp.MustCompile(`\b(?:ba|z|da|k|fi)?sh\s+(?:-\w+\s+)*-\w*c|\beval\s`)

var quoteJoiner = strings.NewReplacer("'", "", `"`, "", `\`, "")

func headCreatesPane(cmd string) bool {
	lit := literalEscapedSeparators(cmd)
	for _, text := range []string{lit, quoteJoiner.Replace(lit)} {
		for _, m := range paneCreate.FindAllStringIndex(text, -1) {
			args := text[m[1]:]
			if i := strings.IndexAny(args, ";&|()'\"<>`\n"); i >= 0 {
				args = args[:i]
			}
			if loc := argsEnd.FindStringIndex(args); loc != nil {
				args = args[:loc[0]]
			}
			if !helpFlag.MatchString(args) {
				return true
			}
		}
	}
	return false
}

// literalEscapedSeparators replaces ; & | behind an odd run of backslashes with '_',
// except on lines that run a nested shell or eval.
func literalEscapedSeparators(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if !nestedShell.MatchString(line) {
			lines[i] = literalLine(line)
		}
	}
	return strings.Join(lines, "\n")
}

func literalLine(s string) string {
	out := []byte(s)
	slashes := 0
	for i, c := range out {
		if c == '\\' {
			slashes++
			continue
		}
		if slashes%2 == 1 && strings.IndexByte(";&|", c) >= 0 {
			out[i] = '_'
		}
		slashes = 0
	}
	return string(out)
}

func runHook(cli string, stdin io.Reader, stdout, stderr io.Writer) int {
	data, _ := io.ReadAll(stdin)
	if cli == "claude-prompt" {
		return goalPromptHook(data, stdout)
	}
	// Debugging aid: with ~/.orch/hook-debug present, append every input per CLI.
	if fileExists(filepath.Join(orchHome(), "hook-debug")) {
		if f, err := os.OpenFile(filepath.Join(orchHome(), "hook-debug-"+cli+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			_, _ = f.Write(append(bytes.TrimSpace(data), '\n'))
			_ = f.Close()
		}
	}
	var in hookInput
	if err := json.Unmarshal(data, &in); err != nil {
		return 0
	}
	reason := decide(in, envOrch("ROLE"), os.Getenv("HERDR_ENV") == "1")
	if reason == "" {
		if cli == "agy" {
			// agy fails closed without a decision. Workers already bypass permissions, so
			// allow is equivalent there; elsewhere ask keeps the normal permission flow.
			decision := "ask"
			if envOrch("ROLE") == "worker" {
				decision = "allow"
			}
			fmt.Fprintf(stdout, "{\"decision\":%q}\n", decision)
		}
		return 0
	}
	switch cli {
	case "agy":
		out, _ := json.Marshal(map[string]string{"decision": "deny", "reason": reason})
		fmt.Fprintln(stdout, string(out))
		return 0
	default: // claude, codex
		out, _ := json.Marshal(map[string]any{
			"hookSpecificOutput": map[string]string{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": reason,
			},
		})
		fmt.Fprintln(stdout, string(out))
		return 0
	}
}

var goalEditTool = regexp.MustCompile(`(?i)(^|[.__])(?:Edit|Write|MultiEdit|NotebookEdit)$`)

func goalPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[2:])
	}
	p, _ = filepath.Abs(p)
	p = filepath.Clean(p)
	// Resolve the existing prefix, retaining any new file or directory suffix.
	ancestor := p
	var suffix []string
	for {
		if real, err := filepath.EvalSymlinks(ancestor); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				real = filepath.Join(real, suffix[i])
			}
			return real
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return p
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
	}
}
func goalScopePath(scope string) string {
	if i := strings.IndexAny(scope, "*?["); i >= 0 {
		if slash := strings.LastIndex(scope[:i], "/"); slash >= 0 {
			return filepath.Join(goalPath(scope[:slash+1]), scope[slash+1:])
		}
	}
	return goalPath(scope)
}
func goalScopeReason(in hookInput) string {
	if !goalEditTool.MatchString(in.tool()) {
		return ""
	}
	g, e := activeGoalFor(os.Getenv("HERDR_PANE_ID"))
	if e != nil || g == nil {
		return ""
	}
	var fields map[string]any
	raw := in.ToolInput
	if len(raw) == 0 && in.ToolCall != nil {
		raw = in.ToolCall.Args
	}
	_ = json.Unmarshal(raw, &fields)
	p, _ := fields["file_path"].(string)
	if p == "" {
		p, _ = fields["notebook_path"].(string)
	}
	if p == "" {
		return ""
	}
	p = goalPath(p)
	home, _ := os.UserHomeDir()
	for _, base := range []string{orchHome(), filepath.Join(home, ".orch"), filepath.Join(home, ".claude"), "/tmp", "/private/tmp"} {
		base = goalPath(base)
		if p == base || strings.HasPrefix(p, base+string(filepath.Separator)) {
			return ""
		}
	}
	for _, scope := range g.Scope {
		if globMatch(goalScopePath(scope), p) {
			return ""
		}
	}
	return fmt.Sprintf("path is outside the active goal's Scope (%s). If it is needed for a criterion, finish it within scope; otherwise record it with orch goal note", g.ID)
}

var goalPrompt = regexp.MustCompile(`(?m)^\s*/goal\b`)

func goalPromptHook(data []byte, stdout io.Writer) int {
	if os.Getenv("HERDR_ENV") != "1" || envOrch("ROLE") == "worker" {
		return 0
	}
	var in struct {
		Prompt string `json:"prompt"`
	}
	if json.Unmarshal(data, &in) != nil {
		return 0
	}
	if !goalPrompt.MatchString(in.Prompt) && !strings.Contains(in.Prompt, "<command-name>/goal</command-name>") {
		return 0
	}
	g, e := activeGoalFor(os.Getenv("HERDR_PANE_ID"))
	if e != nil || g != nil {
		return 0
	}
	quoted := "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "$", "\\$", "`", "\\`").Replace(in.Prompt) + "\""
	text := fmt.Sprintf("Before any other work run orch goal new --text %s (the goal verbatim). Fill goal.md with at most 8 checkable criteria, non-goals, scope and budget, then run orch goal freeze. Pass --serves N on every spawn/do. Record anything else found with orch goal note. Treat the goal as complete when orch goal check exits 0, and as stopped (report results and backlog and ask the user whether to extend) when it exits 3.", quoted)
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "UserPromptSubmit", "additionalContext": text}})
	fmt.Fprintln(stdout, string(out))
	return 0
}
