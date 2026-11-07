package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// This recognizer deliberately supports simple shell words, quoted literals,
// pipelines, and nested -c scripts only. It is a delegation aid, not a sandbox.
// Grammar produces argv and redirects; policy below never reparses shell text.
type headCommand struct {
	argv    []string
	dynamic []bool
	writes  bool
}
type headScanner struct {
	text     string
	i        int
	commands []headCommand
}

func scanHeadCommands(text string) ([]headCommand, error) {
	s := &headScanner{text: text}
	err := s.scan(false, 0)
	return s.commands, err
}
func (s *headScanner) scan(sub bool, depth int) error {
	if depth > 24 {
		return fmt.Errorf("nesting limit")
	}
	var cmd headCommand
	flush := func() {
		if len(cmd.argv) > 0 || cmd.writes {
			s.commands = append(s.commands, cmd)
		}
		cmd = headCommand{}
	}
	for s.i < len(s.text) {
		c := s.text[s.i]
		if strings.ContainsRune(" \t\r", rune(c)) {
			s.i++
			continue
		}
		if c == '#' {
			for s.i < len(s.text) && s.text[s.i] != '\n' {
				s.i++
			}
			continue
		}
		if c == ')' {
			if !sub {
				return fmt.Errorf("grouping")
			}
			s.i++
			flush()
			return nil
		}
		if c == '(' || c == '{' || c == '}' {
			return fmt.Errorf("grouping or function")
		}
		if strings.ContainsRune(";|&\n", rune(c)) {
			flush()
			s.i++
			continue
		}
		// Descriptor prefixes belong to redirects rather than argv.
		j := s.i
		for j < len(s.text) && s.text[j] >= '0' && s.text[j] <= '9' {
			j++
		}
		if j < len(s.text) && (s.text[j] == '>' || s.text[j] == '<') {
			s.i = j
			op := s.text[s.i]
			s.i++
			if s.i < len(s.text) && s.text[s.i] == op {
				if op == '<' {
					return fmt.Errorf("heredoc")
				}
				s.i++
			}
			descriptor := false
			if s.i < len(s.text) && s.text[s.i] == '&' {
				descriptor = true
				s.i++
			}
			for s.i < len(s.text) && (s.text[s.i] == ' ' || s.text[s.i] == '\t') {
				s.i++
			}
			target, dynamic, err := s.word(depth)
			if err != nil {
				return err
			}
			if target == "" || dynamic {
				return fmt.Errorf("dynamic redirect")
			}
			if op == '>' && target != "/dev/null" && !(descriptor && onlyFD(target)) {
				cmd.writes = true
			}
			continue
		}
		// Consume a whole shell word here: word handles quoted/escaped syntax
		// before the outer scanner can interpret it as grouping or separators.
		word, dynamic, err := s.word(depth)
		if err != nil {
			return err
		}

		cmd.argv = append(cmd.argv, word)
		cmd.dynamic = append(cmd.dynamic, dynamic)
	}
	flush()
	if sub {
		return fmt.Errorf("unclosed substitution")
	}
	return nil
}
func onlyFD(s string) bool {
	if s == "-" {
		return true
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}
func (s *headScanner) word(depth int) (string, bool, error) {
	var b strings.Builder
	dynamic := false
	quote := byte(0)
	for s.i < len(s.text) {
		c := s.text[s.i]
		if quote == 0 && strings.ContainsRune(" \t\r\n;|&<>()", rune(c)) {
			break
		}
		s.i++
		if c == '\\' && quote != '\'' {
			if s.i >= len(s.text) {
				return "", false, fmt.Errorf("trailing escape")
			}
			n := s.text[s.i]
			if quote == '"' && !strings.ContainsRune("$`\"\\\n", rune(n)) {
				b.WriteByte(c)
				continue
			}
			s.i++
			if n != '\n' {
				b.WriteByte(n)
			}
			continue
		}
		if c == '\'' || c == '"' {
			if quote == 0 {
				quote = c
				continue
			}
			if quote == c {
				quote = 0
				continue
			}
		}
		if quote == 0 && strings.ContainsRune("*?[{", rune(c)) {
			dynamic = true
		}
		if quote != '\'' && c == '`' {
			return "", false, fmt.Errorf("backtick substitution (use orch)")
		}
		if quote != '\'' && c == '$' {
			dynamic = true
			if s.i < len(s.text) && s.text[s.i] == '(' {
				return "", false, fmt.Errorf("command substitution (use orch)")
			}
		}
		b.WriteByte(c)
	}
	if quote != 0 {
		return "", false, fmt.Errorf("unclosed quote")
	}
	if b.Len() == 0 && s.i < len(s.text) && (s.text[s.i] == '<' || s.text[s.i] == '(') {
		return "", false, fmt.Errorf("process substitution")
	}
	return b.String(), dynamic, nil
}

var shellAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func headExecutable(argv []string) []string {
	for len(argv) > 0 {
		if shellAssignment.MatchString(argv[0]) {
			argv = argv[1:]
			continue
		}
		switch filepath.Base(argv[0]) {
		case "env", "command", "exec", "sudo", "nohup", "time", "builtin":
			argv = argv[1:]
			// Unknown wrapper options require delegation rather than guessing arity.
			if len(argv) > 0 && strings.HasPrefix(argv[0], "-") {
				return []string{"unsupported-wrapper"}
			}
		default:
			return argv
		}
	}
	return nil
}

var headExecutionPrograms = strings.Fields("gofmt make gmake npm npx pnpm yarn bun uv gh pytest ruby perl node cargo rustc gcc clang cmake tee touch mkdir rm mv cp ln chmod dd rsync install curl wget")

func headCommandExecutes(cmd headCommand) bool {
	if cmd.writes {
		return true
	}
	argv := headExecutable(cmd.argv)
	if len(argv) == 0 {
		return false
	}
	p := filepath.Base(argv[0])
	args := argv[1:]
	// Dynamic words are grammar metadata. Only policy decides whether they can
	// select an executable/subcommand or an embedded inspection program.
	offset := len(cmd.argv) - len(argv)
	for i, dynamic := range cmd.dynamic {
		if dynamic && (i == offset || i > offset && contains1([]string{"awk", "sed", "find", "go", "git"}, p)) {
			return true
		}
	}
	if contains1(strings.Fields("! if then elif else fi for while until do done case esac select function repeat coproc [["), p) {
		return true
	}

	if contains1(headExecutionPrograms, p) || strings.HasPrefix(p, "python") || strings.HasPrefix(p, "pip") {
		return true
	}
	switch p {
	case "unsupported-wrapper", "eval", "source", ".", "xargs":
		return true
	case "bash", "sh", "zsh", "dash", "ksh", "fish":
		for i, a := range args {
			if strings.HasPrefix(a, "--") {
				if a == "--norc" || a == "--noprofile" {
					continue
				}
				return true
			}
			if !strings.HasPrefix(a, "-") {
				return true
			}
			if strings.Contains(a, "c") && i+1 < len(args) {
				if cmd.dynamic[offset+i+2] {
					return true
				}
				cs, e := scanHeadCommands(args[i+1])
				if e != nil {
					return true
				}
				for _, c := range cs {
					if headCommandExecutes(c) {
						return true
					}
				}
				return false
			}
		}
		return true
	case "git":
		return headGitExecutes(args)
	case "go":
		if len(args) > 0 && args[0] == "-C" {
			if len(args) < 3 {
				return true
			}
			args = args[2:]
		}
		if len(args) == 0 {
			return false
		}
		if contains1(strings.Fields("test build run install generate fmt vet get"), args[0]) {
			return true
		}
		return !contains1(strings.Fields("version env list doc help"), args[0])
	case "find":
		for _, a := range args {
			if contains1(strings.Fields("-delete -exec -execdir -ok -okdir -fprint -fprint0 -fprintf -fls"), a) {
				return true
			}
		}
	case "sed":
		options := true
		for i := 0; i < len(args); i++ {
			a := args[i]
			program := a
			if options && a == "--" {
				options = false
				continue
			}
			if options && strings.HasPrefix(a, "--") {
				switch {
				case a == "--expression":
					i++
					if i == len(args) {
						return true
					}
					program = args[i]
				case strings.HasPrefix(a, "--expression="):
					program = strings.TrimPrefix(a, "--expression=")
				default:
					return true
				}
			} else if options && strings.HasPrefix(a, "-") && a != "-" {
				program = ""
				for j := 1; j < len(a); j++ {
					switch a[j] {
					case 'n', 'E', 'r':
					case 'e':
						program = a[j+1:]
						if program == "" {
							i++
							if i == len(args) {
								return true
							}
							program = args[i]
						}
						j = len(a)
					default:
						// Includes file, in-place, and unknown options.
						return true
					}
				}
			}
			if inspectionProgramExecution(p, program) {
				return true
			}
		}
	case "awk":
		for _, a := range args {
			if a == "-f" || a == "--file" || strings.HasPrefix(a, "--file=") || strings.HasPrefix(a, "-f") {
				return true
			}
			if inspectionProgramExecution(p, a) {
				return true
			}
		}
	}
	return false
}
func headGitExecutes(args []string) bool {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		a := args[0]
		args = args[1:]
		key, _, equals := strings.Cut(a, "=")
		switch key {
		case "--no-pager", "-P", "--paginate", "-p", "--literal-pathspecs", "--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs", "--bare", "--no-replace-objects", "--no-optional-locks":
			if equals {
				return true
			}
		case "-c", "--config-env":
			return true
		case "-C", "--git-dir", "--work-tree", "--namespace", "--exec-path":
			if !equals {
				if len(args) == 0 {
					return true
				}
				args = args[1:]
			}
		default:
			if strings.HasPrefix(a, "-C") && len(a) > 2 {
				continue
			}
			return true
		}
	}
	if len(args) == 0 {
		return false
	}
	// Inspection commands can still request output writes or pager execution.
	for _, a := range args[1:] {
		if a == "--output" || strings.HasPrefix(a, "--output=") || strings.HasPrefix(a, "-O") || a == "--open-files-in-pager" || strings.HasPrefix(a, "--open-files-in-pager=") {
			return true
		}
	}
	// Only the known inspection subcommands bypass delegation. Unknown commands
	// include aliases, whose implementation cannot be inferred here.
	switch args[0] {
	case "log", "show", "diff", "status", "ls-files", "ls-tree", "rev-parse", "rev-list", "grep", "blame", "describe", "cat-file", "check-ignore", "check-attr", "for-each-ref", "show-ref", "version", "help",
		"shortlog", "merge-base", "name-rev", "count-objects", "diff-tree", "diff-index", "diff-files", "cherry", "range-diff", "whatchanged", "var":
		return false
	case "ls-remote":
		return !gitLsRemoteReadOnly(args[1:])
	case "branch":
		return !gitBranchReadOnly(args[1:])
	case "remote":
		return !gitRemoteReadOnly(args[1:])
	case "stash":
		return len(args) < 2 || !contains1([]string{"list", "show"}, args[1])
	case "tag":
		return !gitTagReadOnly(args[1:])
	case "reflog":
		return len(args) > 1 && args[1] != "show"
	case "worktree":
		return len(args) < 2 || args[1] != "list"
	case "config":
		return !gitConfigReadOnly(args[1:])
	case "symbolic-ref":
		return !gitSymbolicRefReadOnly(args[1:])
	case "submodule":
		return len(args) < 2 || args[1] != "status"
	default:
		return true
	}
}

// Only explicitly listed value options accept the --option=value form.
func gitReadOnlyValueOption(arg string, names ...string) bool {
	key, _, equals := strings.Cut(arg, "=")
	return equals && contains1(names, key)
}

func gitLsRemoteReadOnly(args []string) bool {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "--heads", "-h", "--tags", "-t", "--refs", "-q", "--quiet", "--symref", "--get-url", "--exit-code":
		default:
			if !gitReadOnlyValueOption(arg, "--sort") {
				return false
			}
		}
	}
	return true
}

func gitBranchReadOnly(args []string) bool {
	list, positional, options := false, false, true
	for _, arg := range args {
		if options && arg == "--" {
			options = false
			continue
		}
		if !options || !strings.HasPrefix(arg, "-") {
			positional = true
			continue
		}
		switch arg {
		case "--list", "--merged", "--no-merged", "--contains", "--no-contains", "--points-at":
			list = true
		case "--show-current", "-a", "-r", "--all", "--remotes", "-v", "-vv", "-vvv", "--verbose", "--no-color", "--color", "--column", "--no-column":
		default:
			if !gitReadOnlyValueOption(arg, "--sort", "--format") {
				return false
			}
		}
	}
	return !positional || list
}

func gitRemoteReadOnly(args []string) bool {
	if len(args) > 0 && contains1([]string{"show", "get-url"}, args[0]) {
		return true
	}
	options := true
	for _, arg := range args {
		if options && arg == "--" {
			options = false
			continue
		}
		if !options || !contains1([]string{"-v", "--verbose"}, arg) {
			return false
		}
	}
	return true
}

func gitTagLinesOption(arg string) bool {
	if !strings.HasPrefix(arg, "-n") {
		return false
	}
	for _, c := range arg[2:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func gitTagReadOnly(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if args[0] != "-l" && args[0] != "--list" && !gitTagLinesOption(args[0]) {
		return false
	}
	options := true
	for _, arg := range args[1:] {
		if options && arg == "--" {
			options = false
			continue
		}
		if !options || !strings.HasPrefix(arg, "-") {
			continue
		}
		if contains1([]string{"--contains", "--no-contains", "--merged", "--no-merged", "--points-at"}, arg) ||
			gitTagLinesOption(arg) || gitReadOnlyValueOption(arg, "--sort", "--format") {
			continue
		}
		return false
	}
	return true
}

func gitConfigReadOnly(args []string) bool {
	action, list, options, positional := false, false, true, 0
	for _, arg := range args {
		if options && arg == "--" {
			options = false
			continue
		}
		if !options || !strings.HasPrefix(arg, "-") {
			positional++
			continue
		}
		switch arg {
		case "-l", "--list":
			action, list = true, true
		case "--get", "--get-all", "--get-regexp":
			action = true
		case "--global", "--local", "--system", "--worktree", "--show-origin", "--show-scope", "--null", "-z", "--name-only", "--bool", "--int", "--bool-or-int", "--path":
		default:
			if !gitReadOnlyValueOption(arg, "--type") {
				return false
			}
		}
	}
	return action && (list && positional == 0 || !list && positional <= 1)
}

func gitSymbolicRefReadOnly(args []string) bool {
	positional, options := 0, true
	for _, arg := range args {
		if options && arg == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(arg, "-") {
			if !contains1([]string{"-q", "--quiet", "--short", "--no-recurse", "--recurse"}, arg) {
				return false
			}
		} else {
			positional++
		}
	}
	return positional == 1
}
