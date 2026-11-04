package main

import (
	"path/filepath"
	"regexp"
	"strings"
)

// A practical redirect for known tool names and commands, not a shell sandbox.
var headExecution = regexp.MustCompile(cmdPos + `(\S*/)?(go\s+(-C\s+\S+\s+)?(test|build|run|install|generate|fmt|vet)|gofmt|make|gmake|npm|npx|pnpm|yarn|bun|uv|gh|pytest|python[0-9.]*|ruby|perl|node|pip[0-9.]*|cargo|rustc|gcc|clang|cmake|git\s+((-[Cc]\s+\S+|--(git-dir|work-tree)(=\S+|\s+\S+))\s+)*(add|commit|push|pull|merge|rebase|checkout|switch|reset|restore|clean|apply|cherry-pick|worktree|fetch|stash|tag|rm|mv|revert|am|branch\s+-[dD]|config)|sed\s+-[^\s]*i|tee|touch|mkdir|rm|mv|cp|ln|chmod|dd|rsync|install|curl|wget)\b`)
var headRedirect = regexp.MustCompile(`(^|[^<])>{1,2}`)
var headSafeRedirect = regexp.MustCompile(`\s+[0-9]*>{1,2}\s*/dev/null(?:\s|$)|\s+[0-9]*>&[0-9-]+(?:\s|$)`)
var inspectionStart = regexp.MustCompile(`^\s*(rg|grep|awk|sed|cat|head|tail|sort|uniq|wc|find|git\s+(log|show|diff|status))\b`)
var headExtraExecution = regexp.MustCompile(cmdPos + `(?:\S*/)?(?:go\s+(?:-C\s+\S+\s+)?(?:get\b|mod\s+(?:tidy|download|edit|init|vendor)\b)|find\b[^;|&\n]*\s+-(?:delete|exec|execdir|ok|okdir)\b|(?:ba|z|da|k|fi)?sh\s+(?:-\S+\s+)*[^\s-])`)

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
		cmd, programExecution := delegationCommandText(stripPatchHeredocs(in.commandText()))
		joined := quoteJoiner.Replace(cmd)
		// No blanket orch/raw prefix bypass: chained recognized execution still matches.
		if programExecution || headExecution.MatchString(cmd) || headExecution.MatchString(joined) || headExtraExecution.MatchString(cmd) || headExtraExecution.MatchString(joined) || headRedirect.MatchString(headSafeRedirect.ReplaceAllString(cmd, " ")) {
			return reason
		}
	}
	return ""
}

var delegationStart = regexp.MustCompile(`^\s*(?:(?:\w+=\S+|env|command|exec)\s+)*(?:\S*/)?orch\s+(?:ask|do|test|spawn|goal|help|models|status|contract|run|read|wait|collect|close)\b`)

// Quoted orch arguments are requirements/data, not shell programs. Keep actual
// shell separators, redirects and substitutions visible, including those after
// the delegated invocation. This is deliberately narrower than a shell parser.
func delegationCommandText(cmd string) (string, bool) {
	var out strings.Builder
	programExecution := false
	segment := 0
	for i := 0; i < len(cmd); {
		c := cmd[i]
		if c == '\\' && i+1 < len(cmd) {
			out.WriteString(cmd[i : i+2])
			i += 2
			continue
		}
		if c == '\'' || c == '"' {
			end := i + 1
			for end < len(cmd) {
				if c == '"' && cmd[end] == '\\' && end+1 < len(cmd) {
					end += 2
					continue
				}
				if cmd[end] == c {
					break
				}
				end++
			}
			if end == len(cmd) {
				out.WriteString(cmd[i:])
				break
			}
			body := cmd[i+1 : end]
			prefix := cmd[segment:i]
			delegated := delegationStart.MatchString(prefix)
			if delegated || inspectionStart.MatchString(prefix) {
				if !delegated && inspectionProgramExecution(prefix, body) {
					programExecution = true
				}
				if c == '"' && (strings.Contains(body, "$(") || strings.Contains(body, "`")) {
					out.WriteString(strings.ReplaceAll(body, "`", "("))
				} else {
					out.WriteString("quoted_argument")
				}
			} else {
				out.WriteString(cmd[i : end+1])
			}
			i = end + 1
			continue
		}
		out.WriteByte(c)
		if strings.ContainsRune(";&|\n", rune(c)) {
			segment = i + 1
		}
		i++
	}
	return out.String(), programExecution
}

var awkStart = regexp.MustCompile(`^\s*awk\b`)
var sedStart = regexp.MustCompile(`^\s*sed\b`)
var awkString = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)
var awkExecution = regexp.MustCompile(`\bsystem\s*\(|\|\s*getline\b`)
var awkPrint = regexp.MustCompile(`\b(?:print|printf)\b`)
var sedCommand = regexp.MustCompile(`(?:^|[;\n{}])\s*(?:(?:[0-9]+|\$|/(?:\\.|[^/])*/)(?:\s*,\s*(?:[0-9]+|\$|/(?:\\.|[^/])*/))?\s*)?!?\s*([wes])`)

// Inspect the small executable subset before masking quoted inspection data.
// This intentionally recognizes common awk/sed programs, not arbitrary shell code.
func inspectionProgramExecution(prefix, body string) bool {
	if awkStart.MatchString(prefix) {
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
	if !sedStart.MatchString(prefix) {
		return false
	}
	consumed := 0
	for _, m := range sedCommand.FindAllStringSubmatchIndex(body, -1) {
		if m[0] < consumed {
			continue
		}
		command := body[m[2]]
		if command == 'w' || command == 'e' {
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
