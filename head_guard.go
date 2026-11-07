package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

func headExecutionReason(in hookInput) string {
	// Herdr alone does not imply an orch-managed session. A run or explicit
	// opt-in identifies management; ordinary user terminals remain unaffected.
	if envOrch("RUN") == "" && envOrch("HEAD_GUARD") != "1" {
		if headID() == "" {
			return ""
		}
		if _, err := resolveRun(""); err != nil {
			return ""
		}
	}
	const reason = "Head execution is delegated, including tiny edits, builds, tests and Git. Use orch do for writes, orch ask for research, or orch test for commands; Head may inspect and write run contracts/acceptance/decisions."
	patch := goalPatchTool.MatchString(in.tool()) || goalShellPatch.MatchString(in.commandText())
	if goalPathEditTool.MatchString(in.tool()) || patch {
		paths := goalEditPaths(in, patch)
		if len(paths) == 0 {
			return reason
		}
		id := envOrch("RUN")
		if id == "" {
			id, _ = resolveRun("")
		}
		base := goalPath(runDir(id))
		for _, p := range paths {
			if !filepath.IsAbs(p) && !strings.HasPrefix(p, "~/") {
				p = filepath.Join(in.Cwd, p)
			}
			p = goalPath(p)
			if filepath.Base(p) == "goal.md" {
				if g, err := newestGoal(headID(), "draft"); err == nil && g != nil && p == goalPath(filepath.Join(g.dir(), "goal.md")) {
					continue
				}
			}
			if id == "" {
				return reason
			}
			rel, err := filepath.Rel(base, p)
			if err != nil || strings.HasPrefix(rel, "..") {
				return reason
			}
			if !(strings.HasPrefix(rel, "contracts"+string(filepath.Separator)) || rel == "dag.yaml" || rel == "acceptance.md" || rel == "decisions.md" || rel == "findings.md") {
				return reason
			}
		}
		if !goalShellTool.MatchString(in.tool()) {
			return ""
		}
	}
	if goalShellTool.MatchString(in.tool()) {
		commands, err := scanHeadCommands(stripPatchHeredocs(in.commandText()))
		if err != nil {
			return reason + " Unsupported shell syntax: " + err.Error() + "; delegate the complete command." + blockedCommandSuffix([]string{in.commandText()})
		}
		for _, command := range commands {
			if headCommandExecutes(command) {
				return reason + blockedCommandSuffix(command.argv)
			}
		}
	}
	return ""
}

// Limit the command text by bytes while preserving complete UTF-8 runes.
func blockedCommandSuffix(argv []string) string {
	command := strings.Join(argv, " ")
	if len(command) > 160 {
		command = command[:160]
		for !utf8.ValidString(command) {
			command = command[:len(command)-1]
		}
		command += "…"
	}
	return " Blocked: " + command
}

var awkString = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)
var awkExecution = regexp.MustCompile(`\bsystem\s*\(|\|\s*getline\b`)
var awkPrint = regexp.MustCompile(`\b(?:print|printf)\b`)
var sedCommand = regexp.MustCompile(`(?:^|[;\n{}])\s*(?:(?:[0-9]+|\$|/(?:\\.|[^/])*/)(?:\s*,\s*(?:[0-9]+|\$|/(?:\\.|[^/])*/))?\s*)?!?\s*([wWes])`)

// Inspect the small executable subset of already-tokenized program arguments.
// This intentionally recognizes common awk/sed programs, not arbitrary shell code.
func inspectionProgramExecution(prefix, body string) bool {
	if prefix == "awk" {
		program := awkString.ReplaceAllString(body, "quoted_string")
		if awkExecution.MatchString(program) {
			return true
		}
		for _, m := range awkPrint.FindAllStringIndex(program, -1) {
			depth := 0
		statement:
			for _, c := range program[m[1]:] {
				switch c {
				case '(', '[':
					depth++
				case ')', ']':
					if depth > 0 {
						depth--
					}
				case ';', '\n', '}':
					break statement
				case '>', '|':
					// Parenthesized print comparisons are expressions, not redirects.
					if depth == 0 {
						return true
					}
				}
			}
		}
		return false
	}
	if prefix != "sed" {
		return false
	}
	consumed := 0
	for _, m := range sedCommand.FindAllStringSubmatchIndex(body, -1) {
		if m[0] < consumed {
			continue
		}
		command := body[m[2]]
		if command == 'w' || command == 'W' || command == 'e' {
			return true
		}
		// A substitution has two delimiter-terminated fields; only its flags
		// execute/write. Letters in patterns and replacements remain data.
		i := m[3]
		if i >= len(body) {
			continue
		}
		delim := body[i]
		i++
		fields := 0
		for i < len(body) && fields < 2 {
			if body[i] == '\\' && i+1 < len(body) {
				i += 2
				continue
			}
			if body[i] == delim {
				fields++
			}
			i++
		}
		if fields == 2 {
			consumed = i
			flags := body[i:]
			if end := strings.IndexAny(flags, ";\n}"); end >= 0 {
				flags = flags[:end]
			}
			if strings.ContainsAny(flags, "we") {
				return true
			}
		}
	}
	return false
}
