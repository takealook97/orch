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
	cmd += "\n" + strings.NewReplacer("'", "", `"`, "", `\`, "").Replace(cmd)
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
	if strings.Contains(cmd, "ORCH_RAW_HERDR=1") {
		return ""
	}
	switch {
	case subagentTools.MatchString(tool):
		return "Inside Herdr, Head delegates through orch instead of built-in subagents: orch run new → orch contract new → orch spawn --model <key> → orch prompt → orch wait → orch collect → orch close. In direct mode, work directly."
	case paneCreate.MatchString(cmd):
		return "Create worker panes only with orch spawn (fixed model, layout, ORCH_ROLE injection). For a necessary non-delegation command pane, prefix the command with ORCH_RAW_HERDR=1."
	}
	return ""
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
