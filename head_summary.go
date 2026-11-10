package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// HeadSession contains only identifiers, timestamps and counts. Tool arguments
// and user text are inspected transiently and never enter the persisted schema.
type HeadSession struct {
	Version           int       `json:"version"`
	Session           string    `json:"session"`
	Model             string    `json:"model"`
	FirstTS           time.Time `json:"first_ts"`
	LastTS            time.Time `json:"last_ts"`
	Requests          int       `json:"requests"`
	MaxContext        *int64    `json:"max_context"`
	InputSum          *int64    `json:"input_sum"`
	OutputSum         *int64    `json:"output_sum"`
	HumanTurns        int       `json:"human_turns"`
	NotificationTurns int       `json:"notification_turns"`
	Delegations       int       `json:"delegations"`
	Waits             int       `json:"waits"`
	OrchOther         int       `json:"orch_other"`
	InspectBash       int       `json:"inspect_bash"`
	BashOther         int       `json:"bash_other"`
	Reads             int       `json:"reads"`
	Edits             int       `json:"edits"`
}

// Input stays raw: only a Bash command that is a JSON string is ever decoded.
type headContent struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
}

// headBashCommand returns the command only when input is an object whose
// command field is a JSON string; every other shape is unclassifiable.
func headBashCommand(input json.RawMessage) (string, bool) {
	var fields struct {
		Command json.RawMessage `json:"command"`
	}
	if json.Unmarshal(input, &fields) != nil {
		return "", false
	}
	var command string
	if json.Unmarshal(fields.Command, &command) != nil {
		return "", false
	}
	return command, true
}

// headOrchPhrase reports whether "orch <sub>" occurs followed by a word
// boundary. Quote characters count as boundaries so quoted mentions still match.
func headOrchPhrase(command, sub string) bool {
	phrase := "orch " + sub
	for rest := command; ; {
		i := strings.Index(rest, phrase)
		if i < 0 {
			return false
		}
		rest = rest[i+len(phrase):]
		if rest == "" || strings.ContainsRune(" \t\n\r;&|)'\"`", rune(rest[0])) {
			return true
		}
	}
}

type headTranscriptRecord struct {
	Type      string    `json:"type"`
	Version   *int      `json:"schema_version"`
	Sidechain bool      `json:"isSidechain"`
	Meta      bool      `json:"isMeta"`
	Session   string    `json:"sessionId"`
	UUID      string    `json:"uuid"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Stop    string          `json:"stop_reason"`
		Usage   *providerTokens `json:"usage"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// Count one category per Bash tool block. Preserve the phase-0 phrase-matching
// rule for orch calls, including mentions in quoted data. Delegation takes
// precedence over wait, then other orch calls, then the first plain executable.
func headBashCategory(command string) string {
	for _, sub := range []string{"ask", "do", "spawn", "prompt", "test", "dag run", "merge"} {
		if headOrchPhrase(command, sub) {
			return "delegations"
		}
	}
	if headOrchPhrase(command, "wait") {
		return "waits"
	}
	if strings.Contains(command, "orch ") {
		return "orch_other"
	}
	commands, err := scanHeadCommands(command)
	if err != nil || len(commands) == 0 {
		return "bash_other"
	}
	c := commands[0]
	argv := headExecutable(c.argv)
	if len(argv) > 0 && !c.dynamic[len(c.argv)-len(argv)] && contains1([]string{"grep", "rg", "cat", "sed", "head", "tail", "ls", "find", "wc", "awk", "jq", "git"}, filepath.Base(argv[0])) {
		return "inspect_bash"
	}
	return "bash_other"
}

func collectHeadSession(session string, r io.Reader) (HeadSession, error) {
	h := HeadSession{Version: 1, Session: session}
	if !evidenceID.MatchString(session) {
		return h, fmt.Errorf("invalid head session identity")
	}
	reader := bufio.NewReader(r)
	final := map[string]headTranscriptRecord{}
	// Claude Code splits one message into a final record per content block, so
	// tool blocks merge across records of a message id, deduped by tool_use id.
	tools := map[string][]headContent{}
	toolSeen := map[string]bool{}
	users := map[string]bool{}
	for {
		line, err := readUsageLine(reader)
		if err == io.EOF {
			break // A growing, non-newline-closed record is not evidence yet.
		}
		if err != nil {
			return h, err
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var rec headTranscriptRecord
		if json.Unmarshal(line, &rec) != nil {
			return h, fmt.Errorf("invalid head transcript JSON or timestamp")
		}
		if rec.Version != nil && *rec.Version != 1 {
			return h, fmt.Errorf("unsupported head transcript version")
		}
		if rec.Session != "" && rec.Session != session {
			return h, fmt.Errorf("provider session binding mismatch")
		}
		// Ignore unbound local metadata instead of attributing it to this Head.
		if rec.Session == "" || rec.Sidechain {
			continue
		}
		if !rec.Timestamp.IsZero() {
			if h.FirstTS.IsZero() || rec.Timestamp.Before(h.FirstTS) {
				h.FirstTS = rec.Timestamp
			}
			if rec.Timestamp.After(h.LastTS) {
				h.LastTS = rec.Timestamp
			}
		}
		if rec.Type == "assistant" && rec.Message.Stop != "" && rec.Message.Model != "<synthetic>" {
			if !evidenceID.MatchString(rec.Message.ID) || rec.Message.Model != "" && !evidenceID.MatchString(rec.Message.Model) {
				return h, fmt.Errorf("invalid head message or model identity")
			}
			final[rec.Message.ID] = rec
			var blocks []headContent
			if len(rec.Message.Content) > 0 && json.Unmarshal(rec.Message.Content, &blocks) != nil {
				return h, fmt.Errorf("invalid head assistant content shape")
			}
			for _, b := range blocks {
				if b.Type != "tool_use" {
					continue
				}
				if b.ID != "" {
					key := rec.Message.ID + "\x00" + b.ID
					if toolSeen[key] {
						continue
					}
					toolSeen[key] = true
				}
				tools[rec.Message.ID] = append(tools[rec.Message.ID], b)
			}
		}
		if rec.Type != "user" || rec.Meta {
			continue
		}
		if rec.UUID != "" {
			if users[rec.UUID] {
				continue
			}
			users[rec.UUID] = true
		}
		var text string
		if json.Unmarshal(rec.Message.Content, &text) != nil {
			var blocks []headContent
			if json.Unmarshal(rec.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type == "text" {
					text += b.Text + "\n"
				}
			}
		}
		text = strings.TrimSpace(text)
		if text == "" || strings.HasPrefix(text, "<command-") || strings.HasPrefix(text, "<local-command") || strings.HasPrefix(text, "<system-reminder>") {
			continue // Tool results and command/hook injections are not human turns.
		}
		if strings.HasPrefix(strings.TrimSpace(text), "<task-notification>") {
			h.NotificationTurns++
		} else {
			h.HumanTurns++ // Wakeups without an explicit marker fold into human.
		}
	}
	models := map[string]int{}
	add := func(dst **int64, n *int64) error {
		if n == nil {
			return nil
		}
		if *dst == nil {
			*dst = new(int64)
		}
		if **dst > math.MaxInt64-*n {
			return fmt.Errorf("head usage sum overflow")
		}
		**dst += *n
		return nil
	}
	for id, rec := range final {
		h.Requests++
		if rec.Message.Model != "" {
			models[rec.Message.Model]++
		}
		if rec.Message.Usage != nil {
			in, out, err := normalizeProviderTokens(rec.Message.Usage, true)
			if err != nil {
				return h, err
			}
			if err = add(&h.InputSum, in); err != nil {
				return h, err
			}
			if err = add(&h.OutputSum, out); err != nil {
				return h, err
			}
			if in != nil && (h.MaxContext == nil || *in > *h.MaxContext) {
				v := *in
				h.MaxContext = &v
			}
		}
		for _, b := range tools[id] {
			switch b.Name {
			case "Bash":
				category := "bash_other"
				if command, ok := headBashCommand(b.Input); ok {
					category = headBashCategory(command)
				}
				switch category {
				case "delegations":
					h.Delegations++
				case "waits":
					h.Waits++
				case "orch_other":
					h.OrchOther++
				case "inspect_bash":
					h.InspectBash++
				default:
					h.BashOther++
				}
			case "Read", "Grep", "Glob":
				h.Reads++
			case "Write", "Edit", "NotebookEdit":
				h.Edits++
			}
		}
	}
	for model, count := range models {
		if h.Model == "" || count > models[h.Model] || count == models[h.Model] && model < h.Model {
			h.Model = model // Stable lexical tie break, independent of map iteration.
		}
	}
	return h, nil
}

func headSessionPath() string {
	return filepath.Join(orchHome(), "measurements", "head_sessions.jsonl")
}

func (h HeadSession) validate() error {
	if h.Version != 1 || !evidenceID.MatchString(h.Session) || h.Model != "" && !evidenceID.MatchString(h.Model) {
		return fmt.Errorf("invalid head summary identity or version")
	}
	if h.LastTS.Before(h.FirstTS) {
		return fmt.Errorf("invalid head summary timestamp range")
	}
	for _, n := range []int{h.Requests, h.HumanTurns, h.NotificationTurns, h.Delegations, h.Waits, h.OrchOther, h.InspectBash, h.BashOther, h.Reads, h.Edits} {
		if n < 0 {
			return fmt.Errorf("negative head summary count")
		}
	}
	for _, n := range []*int64{h.MaxContext, h.InputSum, h.OutputSum} {
		if n != nil && *n < 0 {
			return fmt.Errorf("negative head summary usage")
		}
	}
	return nil
}

// headSummaryCompactBytes triggers a rewrite to one selected row per session.
const headSummaryCompactBytes = 4 << 20

// headSummaryMaxLine bounds one summary row; longer lines are skipped.
const headSummaryMaxLine = 1 << 20

// headSelected implements row selection: greatest last_ts, then greatest
// requests, then the later append (candidate is the later row).
func headSelected(current, candidate HeadSession) bool {
	if !candidate.LastTS.Equal(current.LastTS) {
		return candidate.LastTS.After(current.LastTS)
	}
	return candidate.Requests >= current.Requests
}

var errHeadLineTooLong = errors.New("head summary line too long")

// readHeadLine returns one line, or errHeadLineTooLong after discarding the rest
// of a line above the limit, so one corrupt row never stops the read.
func readHeadLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		part, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(part) > headSummaryMaxLine {
				tooLong = true
				line = nil
			} else {
				line = append(line, part...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if tooLong && (err == nil || err == io.EOF) {
			return nil, errHeadLineTooLong
		}
		return line, err
	}
}

// readHeadSessions treats the file as statistics, not evidence: malformed or
// invalid lines, including an unterminated tail, are skipped and counted.
// Only I/O failures are errors, and their messages are fixed strings.
func readHeadSessions(r io.Reader) (map[string]HeadSession, int, error) {
	rows := map[string]HeadSession{}
	skipped := 0
	reader := bufio.NewReader(r)
	for {
		line, err := readHeadLine(reader)
		if err == errHeadLineTooLong {
			skipped++
			continue
		}
		if err == io.EOF {
			if len(strings.TrimSpace(string(line))) > 0 {
				skipped++
			}
			return rows, skipped, nil
		}
		if err != nil {
			return nil, skipped, fmt.Errorf("cannot read head summary file")
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var h HeadSession
		if json.Unmarshal(line, &h) != nil || h.validate() != nil {
			skipped++
			continue
		}
		if current, ok := rows[h.Session]; !ok || headSelected(current, h) {
			rows[h.Session] = h
		}
	}
}

// openHeadSummaryLocked opens the summary file under an exclusive lock and
// retries when a concurrent rewrite replaced the path while this call waited.
func openHeadSummaryLocked(p string) (*os.File, error) {
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return nil, err
		}
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			return nil, err
		}
		held, err1 := f.Stat()
		named, err2 := os.Stat(p)
		if err1 == nil && err2 == nil && os.SameFile(held, named) {
			return f, nil
		}
		f.Close() // Closing releases the lock on the replaced inode.
		if err1 != nil {
			return nil, err1
		}
		if err2 != nil && !os.IsNotExist(err2) {
			return nil, err2
		}
	}
}

// rewriteHeadSummary replaces the file with the selected row per session plus
// h, via a temporary file in the same directory and rename. The caller holds
// the exclusive lock on f.
func rewriteHeadSummary(f *os.File, p string, h HeadSession) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	rows, _, err := readHeadSessions(f)
	if err != nil {
		return err
	}
	sessions := make([]string, 0, len(rows))
	for s := range rows {
		if s != h.Session {
			sessions = append(sessions, s)
		}
	}
	sort.Strings(sessions)
	out := make([]HeadSession, 0, len(sessions)+2)
	for _, s := range sessions {
		out = append(out, rows[s])
	}
	if current, ok := rows[h.Session]; ok {
		out = append(out, current) // Keep append semantics: h follows the prior row.
	}
	out = append(out, h)
	tmp, err := os.CreateTemp(filepath.Dir(p), ".head_sessions-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0600); err != nil {
		return err
	}
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	for _, row := range out {
		if err = enc.Encode(row); err != nil {
			return err
		}
	}
	if err = w.Flush(); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpName, p); err != nil {
		return err
	}
	keep = true
	return nil
}

// recordHeadSession appends without validating existing rows. A partial last
// line from an interrupted writer is closed with a newline first.
func recordHeadSession(h HeadSession) error {
	if err := h.validate(); err != nil {
		return err
	}
	p := headSessionPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	f, err := openHeadSummaryLocked(p)
	if err != nil {
		return err
	}
	defer f.Close()
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > headSummaryCompactBytes {
		return rewriteHeadSummary(f, p, h)
	}
	if info.Size() > 0 {
		last := make([]byte, 1)
		if _, err = f.ReadAt(last, info.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			if _, err = f.Write([]byte("\n")); err != nil {
				return err
			}
		}
	}
	if err = json.NewEncoder(f).Encode(h); err != nil {
		return err
	}
	return f.Sync()
}

func collectHeadSessionFile(session, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("cannot open head transcript file")
	}
	defer f.Close()
	h, err := collectHeadSession(session, f)
	if err != nil {
		return err
	}
	return recordHeadSession(h)
}

type headStats struct {
	Sessions              int      `json:"sessions"`
	Requests              int      `json:"requests"`
	MedianMaxContext      *float64 `json:"median_max_context"`
	MaxContext            *int64   `json:"max_context"`
	Delegations           int      `json:"delegations"`
	InspectBash           int      `json:"inspect_bash"`
	RequestsPerDelegation *float64 `json:"requests_per_delegation"`
	Skipped               int      `json:"skipped"`
}

// collectHeadStats returns only fixed-string errors so callers can print them.
func collectHeadStats(cutoff time.Time) (headStats, error) {
	var stats headStats
	f, err := os.Open(headSessionPath())
	if os.IsNotExist(err) {
		return stats, nil
	}
	if err != nil {
		return stats, fmt.Errorf("cannot open head summary file")
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return stats, fmt.Errorf("cannot lock head summary file")
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	rows, skipped, err := readHeadSessions(f)
	stats.Skipped = skipped
	if err != nil {
		return stats, err
	}
	var contexts []int64
	var delegRequests, delegations int
	for _, h := range rows {
		if h.LastTS.IsZero() || h.LastTS.Before(cutoff) {
			continue
		}
		stats.Sessions++
		stats.Requests += h.Requests
		stats.Delegations += h.Delegations
		stats.InspectBash += h.InspectBash
		if h.MaxContext != nil {
			contexts = append(contexts, *h.MaxContext)
		}
		if h.Delegations > 0 {
			delegRequests += h.Requests
			delegations += h.Delegations
		}
	}
	if len(contexts) > 0 {
		sort.Slice(contexts, func(i, j int) bool { return contexts[i] < contexts[j] })
		max := contexts[len(contexts)-1]
		stats.MaxContext = &max
		mid := len(contexts) / 2
		median := float64(contexts[mid])
		if len(contexts)%2 == 0 {
			median = float64(contexts[mid-1])/2 + float64(contexts[mid])/2
		}
		stats.MedianMaxContext = &median
	}
	if delegations > 0 {
		ratio := float64(delegRequests) / float64(delegations)
		stats.RequestsPerDelegation = &ratio
	}
	return stats, nil
}
