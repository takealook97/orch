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

var paneCreate = regexp.MustCompile(cmdPos + `(\S*/)?(herdr\s+(pane\s+split|agent\s+start|tab\s+create|workspace\s+create|worktree\s+create)|herdr-app\s+do\s+(pane\.split|tab\.new|space\.new|terminal\.open|terminal\.keep))\b`)

var hcRunStart = regexp.MustCompile(cmdPos + `(\S*/)?hc\s+run\s+`)
var hcWord = regexp.MustCompile(`'[^']*'|"[^"]*"|[^\s]+`)

func hcRunsAgent(cmd string) bool {
	for _, m := range hcRunStart.FindAllStringIndex(cmd, -1) {
		tail := cmd[m[1]:]
		if end := strings.IndexAny(tail, ";|&\n"); end >= 0 {
			tail = tail[:end]
		}
		words := hcWord.FindAllString(tail, -1)
		for i := range words {
			words[i] = strings.Trim(words[i], `'"`)
		}
		i := 0
		for i < len(words) {
			w := words[i]
			if w == "--help" || w == "-h" {
				i = len(words)
				break
			}
			if w == "--" {
				i++
				break
			}
			if !strings.HasPrefix(w, "-") {
				break
			}
			i++
			if (w == "--cwd" || w == "--label" || w == "--name" || w == "--on" || w == "--parent" || w == "--timeout") && i < len(words) {
				i++
			}
		}
		if i >= len(words) {
			continue
		}
		program := filepath.Base(words[i])
		i++
		if program == "env" {
			for i < len(words) && (strings.Contains(words[i], "=") || strings.HasPrefix(words[i], "-")) {
				i++
			}
			if i >= len(words) {
				continue
			}
			program = filepath.Base(words[i])
			i++
		}
		if program == "sh" || program == "bash" || program == "zsh" {
			if i+1 < len(words) && strings.Contains(words[i], "c") {
				script := strings.Fields(words[i+1])
				if len(script) > 0 {
					program = filepath.Base(script[0])
				}
			}
		}
		switch program {
		case "claude", "codex", "agy", "gemini", "cr":
			return true
		case "npx", "bunx":
			if i < len(words) && (words[i] == "@openai/codex" || words[i] == "@anthropic-ai/claude-code") {
				return true
			}
		}
	}
	return false
}

var orchDelegate = regexp.MustCompile(cmdPos + `(\S*/)?orch\s+(spawn|prompt|run\s+new|fallback)\b`)

// A worker may not drive orch at all (close, resume, gc, run end included).
var orchAny = regexp.MustCompile(cmdPos + `(\S*/)?orch\s+[a-z]`)

var agentCLI = regexp.MustCompile(cmdPos + `(\S*/)?(codex\s+(exec|review|e)\b|claude\s+(-p|--print)\b|agy\s+(-p|--print)\b|gemini\s+-p\b)`)

type hookInput struct {
	TranscriptPath string          `json:"transcript_path"`
	SessionID      string          `json:"session_id"`
	Cwd            string          `json:"cwd"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolCall       *struct {
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

var bashCommandTool = regexp.MustCompile(`(?i)(^|[.__])Bash$`)

// commandText reads only command for Bash; descriptions and other metadata are data.
// Other tools retain their CLI-specific shapes (cmd, CommandLine, argv arrays).
func (h hookInput) commandText() string {
	// An edit body is data even if it contains shell commands or patch examples.
	if goalEditTool.MatchString(h.tool()) {
		return ""
	}
	raw := h.ToolInput
	if len(raw) == 0 && h.ToolCall != nil {
		raw = h.ToolCall.Args
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	if bashCommandTool.MatchString(h.tool()) {
		// Some clients encode the entire argument object as a JSON string.
		for {
			encoded, ok := v.(string)
			if !ok || json.Unmarshal([]byte(encoded), &v) != nil {
				break
			}
		}
		if args, ok := v.(map[string]any); ok {
			command, _ := args["command"].(string)
			return command
		}
		return ""
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
	if role == "worker" {
		// The worker no-delegation policy retains its legacy strict matcher.
		cmd += "\n" + quoteJoiner.Replace(cmd)
		// File edits start no process; their text (source, docs) may name orch or herdr commands.
		if goalEditTool.MatchString(tool) || goalPatchTool.MatchString(tool) {
			return ""
		}
		cmd = stripPatchHeredocs(cmd)
		switch {
		case subagentTools.MatchString(tool):
			return "orch workers cannot delegate. Work within the contract scope and list any blockers in the result report."
		case paneCreate.MatchString(cmd) || hcRunsAgent(cmd) || orchDelegate.MatchString(cmd) || orchAny.MatchString(cmd):
			return "orch workers cannot create panes, agents, or orch delegation. List any blockers in the result report."
		case agentCLI.MatchString(cmd):
			return "orch workers cannot call another agent CLI (no delegation)."
		}
		return ""
	}
	if !inHerdr {
		if envOrch("RUN") != "" {
			return headExecutionReason(in)
		}
		return ""
	}
	if reason := headExecutionReason(in); reason != "" {
		return reason
	}
	if reason := codexGoalCompileReason(in); reason != "" {
		return reason
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
		if hcRunsAgent(text) {
			return true
		}
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
	// Debugging aid: with ~/.orch/hook-debug present, append every input per CLI.
	if fileExists(filepath.Join(orchHome(), "hook-debug")) {
		if f, err := os.OpenFile(filepath.Join(orchHome(), "hook-debug-"+cli+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
			_, _ = f.Write(append(bytes.TrimSpace(data), '\n'))
			_ = f.Close()
		}
	}
	if cli == "claude-prompt" || cli == "codex-prompt" {
		return goalPromptHook(data, stdout)
	}
	var in hookInput
	if err := json.Unmarshal(data, &in); err != nil {
		return 0
	}
	reason := decide(in, envOrch("ROLE"), inHerdr())
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

var goalPatchTool = regexp.MustCompile(`^(?:apply_patch|functions\.apply_patch)$`)
var goalPatchPath = regexp.MustCompile(`(?m)^\*\*\* (?:Add File|Update File|Delete File|Move to):[ \t]*(.+)$`)
var goalShellPatch = regexp.MustCompile(`\bapply_patch\b[^\n]*<<`)

// An apply_patch heredoc's body is file content, not commands.
var patchHeredoc = regexp.MustCompile(`\bapply_patch[ \t]+<<-?[ \t]*(?:'(\w+)'|"(\w+)")([^\n]*)\n`)

// stripPatchHeredocs drops the body of each apply_patch heredoc, up to its terminator line.
// Text outside such a heredoc, including header chains, is still checked.
// Unclosed heredocs are left intact so the scanner rejects them.
func stripPatchHeredocs(cmd string) string {
	var b strings.Builder
	for {
		m := patchHeredoc.FindStringSubmatchIndex(cmd)
		if m == nil {
			b.WriteString(cmd)
			return b.String()
		}
		rest := cmd[m[1]:]
		termStart, termEnd := m[2], m[3]
		if termStart < 0 {
			termStart, termEnd = m[4], m[5]
		}
		term := cmd[termStart:termEnd]
		end := -1
		for off := 0; off < len(rest); {
			line := rest[off:]
			n := strings.IndexByte(line, '\n')
			if n < 0 {
				n = len(line)
			}
			if strings.TrimSpace(line[:n]) == term {
				end = off + n
				if end < len(rest) {
					end++
				}
				break
			}
			off += n + 1
		}
		if end < 0 {
			b.WriteString(cmd)
			return b.String()
		}
		b.WriteString(cmd[:m[0]])
		b.WriteString("apply_patch")
		b.WriteString(cmd[m[6]:m[7]])
		b.WriteByte('\n')
		cmd = rest[end:]
	}
}

// Only known edit APIs may exclude their bodies from command inspection.
// Arbitrary MCP suffixes such as terminal.write can execute commands.
var goalEditTool = regexp.MustCompile(`^(?:Edit|Write|MultiEdit|NotebookEdit|write_file|edit_file)$`)

// Path enforcement is separate from command-body exemptions. Recognizing an MCP
// filesystem edit must never exempt terminal.write or arbitrary suffix matches.
var goalPathEditTool = regexp.MustCompile(`^(?:Edit|Write|MultiEdit|NotebookEdit|write_file|edit_file|mcp__filesystem__(?:write_file|edit_file))$`)

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
func goalEditPaths(in hookInput, patch bool) []string {
	var paths []string
	if patch {
		for _, match := range goalPatchPath.FindAllStringSubmatch(in.commandText(), -1) {
			if p := strings.TrimSpace(match[1]); p != "" {
				paths = append(paths, p)
			}
		}
	} else {
		var fields map[string]any
		raw := in.ToolInput
		if len(raw) == 0 && in.ToolCall != nil {
			raw = in.ToolCall.Args
		}
		_ = json.Unmarshal(raw, &fields)
		p, _ := fields["file_path"].(string)
		for _, key := range []string{"path", "TargetFile", "target_file", "file", "absolute_path"} {
			if p == "" {
				p, _ = fields[key].(string)
			}
		}
		if p == "" {
			p, _ = fields["notebook_path"].(string)
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

var goalCompileCommand = regexp.MustCompile(cmdPos + `(\S*/)?orch\s+(goal|help|models|status)\b`)
var goalShellTool = regexp.MustCompile(`(?i)(^|[.__])(?:Bash|exec_command|shell|shell_command|run_shell_command|run_command|terminal)$`)

func codexGoalCompileReason(in hookInput) string {
	if in.TranscriptPath == "" {
		return ""
	}
	if g, err := activeGoalFor(headID()); err != nil {
		if goalShellTool.MatchString(in.tool()) && goalCompileCommand.MatchString(in.commandText()) {
			return ""
		}
		return "Goal state unavailable: " + err.Error()
	} else if g != nil {
		return ""
	}
	objective, active := codexGoalState(in.TranscriptPath)
	if !active {
		return ""
	}
	if endedGoalMatches(headID(), objective) {
		return ""
	}
	if goalShellTool.MatchString(in.tool()) && goalCompileCommand.MatchString(in.commandText()) {
		return ""
	}
	patch := goalPatchTool.MatchString(in.tool()) || goalShellPatch.MatchString(in.commandText())
	if patch || goalPathEditTool.MatchString(in.tool()) {
		paths := goalEditPaths(in, patch)
		allowed := len(paths) > 0
		base := goalPath(orchHome())
		for _, p := range paths {
			if !filepath.IsAbs(p) && !strings.HasPrefix(p, "~/") && in.Cwd != "" {
				p = filepath.Join(in.Cwd, p)
			}
			p = goalPath(p)
			if p != base && !strings.HasPrefix(p, base+string(filepath.Separator)) {
				allowed = false
			}
		}
		if allowed {
			return ""
		}
	}
	return fmt.Sprintf(`A Codex /goal is active but no orch goal is frozen for this pane. First run: orch goal new --text "/goal %s"; fill goal.md with at most 8 checkable criteria, non-goals, scope and budget; `+`orch goal freeze. Then pass --serves N on every spawn/do and log anything else with orch goal note. Format: numbered criteria '1. text' each with an indented '- check: <shell command>' line; '## Scope' bullets of globs; '## Budget' with numeric hours and max-workers. Edit the template sections in place.`, objective)
}

func goalScopeReason(in hookInput) string {
	patch := goalPatchTool.MatchString(in.tool()) || goalShellPatch.MatchString(in.commandText())
	if !goalPathEditTool.MatchString(in.tool()) && !patch {
		return ""
	}
	g, e := activeGoalFor(headID())
	if e != nil {
		return "Goal state unavailable: " + e.Error()
	}
	if g == nil {
		return ""
	}
	paths := goalEditPaths(in, patch)
	if len(paths) == 0 {
		return "Goal scope cannot be checked: edit tool supplied no recognized file path"
	}
	home, _ := os.UserHomeDir()
	for _, p := range paths {
		if !filepath.IsAbs(p) && !strings.HasPrefix(p, "~/") && in.Cwd != "" {
			p = filepath.Join(in.Cwd, p)
		}
		p = goalPath(p)
		allowed := false
		for _, base := range []string{orchHome(), filepath.Join(home, ".orch"), filepath.Join(home, ".claude"), "/tmp", "/private/tmp"} {
			base = goalPath(base)
			if p == base || strings.HasPrefix(p, base+string(filepath.Separator)) {
				allowed = true
				break
			}
		}
		for _, scope := range g.Scope {
			if globMatch(goalScopePath(scope), p) {
				allowed = true
				break
			}
		}
		if allowed {
			continue
		}
		return fmt.Sprintf("path is outside the active goal's Scope (%s). If it is needed for a criterion, finish it within scope; otherwise record it with orch goal note", g.ID)
	}
	return ""
}

var goalPrompt = regexp.MustCompile(`(?m)^\s*/goal\b`)

func goalPromptHook(data []byte, stdout io.Writer) int {
	if !inHerdr() || envOrch("ROLE") == "worker" {
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
	g, e := activeGoalFor(headID())
	if e != nil {
		out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "UserPromptSubmit", "additionalContext": "Goal state unavailable: " + e.Error()}})
		fmt.Fprintln(stdout, string(out))
		return 0
	}
	if g != nil {
		return 0
	}
	quoted := "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "$", "\\$", "`", "\\`").Replace(in.Prompt) + "\""
	text := fmt.Sprintf("Before any other work run orch goal new --text %s (the goal verbatim). Fill goal.md with at most 8 checkable criteria, non-goals, scope and budget, then run orch goal freeze. Pass --serves N on every spawn/do. Record anything else found with orch goal note. Treat the goal as complete when orch goal check exits 0, and as stopped (report results and backlog and ask the user whether to extend) when it exits 3. Format: numbered criteria '1. text' each with an indented '- check: <shell command>' line; '## Scope' bullets of globs; '## Budget' with numeric hours and max-workers. Edit the template sections in place.", quoted)
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "UserPromptSubmit", "additionalContext": text}})
	fmt.Fprintln(stdout, string(out))
	return 0
}
