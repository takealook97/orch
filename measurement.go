package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Measurement deliberately has no prompt, transcript body or arbitrary note fields.
type Measurement struct {
	Provenance *usageProvenance `json:"provenance,omitempty"`
	Version    int              `json:"version"`
	ID         string           `json:"id"`
	Session    string           `json:"session"`
	Period     string           `json:"period"`
	Subject    string           `json:"subject"`
	Entity     string           `json:"entity"`
	Phase      string           `json:"phase"`
	Source     string           `json:"source"`
	Model      string           `json:"model,omitempty"`
	Kind       string           `json:"kind"`
	Coverage   string           `json:"coverage"`
	Input      *int64           `json:"input_tokens"`
	Output     *int64           `json:"output_tokens"`
	DurationMS *int64           `json:"duration_ms"`
	Event      string           `json:"event,omitempty"`
	Reason     string           `json:"reason,omitempty"`
	Related    string           `json:"related,omitempty"`
	At         time.Time        `json:"at"`
}

var evidenceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,159}$`)

func (m Measurement) validate() error {
	if m.Provenance != nil {
		p := m.Provenance
		for _, id := range []string{p.Message, p.Request} {
			if id != "" && !evidenceID.MatchString(id) {
				return fmt.Errorf("invalid provider observation identity")
			}
		}
		if !contains1([]string{"claude", "codex"}, p.Provider) || p.Binding != "operator-declared" || p.Session != m.Session || len(p.File) != 64 {
			return fmt.Errorf("invalid usage provenance")
		}
		for _, c := range p.File {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return fmt.Errorf("invalid file fingerprint")
			}
		}
	}
	if m.Model != "" && !evidenceID.MatchString(m.Model) {
		return fmt.Errorf("invalid model identity")
	}
	for _, s := range []string{m.ID, m.Session, m.Period, m.Entity, m.Source} {
		if !evidenceID.MatchString(s) {
			return fmt.Errorf("evidence identity/source must be 1-160 identifier characters (no prose)")
		}
	}
	if m.Version != 1 || !contains1([]string{"head", "pane", "task"}, m.Subject) || !contains1([]string{"contract", "execution", "review", "rework"}, m.Phase) || !contains1([]string{"measured", "estimate", "missing"}, m.Kind) || !contains1([]string{"complete", "partial", "unavailable"}, m.Coverage) {
		return fmt.Errorf("invalid evidence version, subject, phase, kind or coverage")
	}
	for _, n := range []*int64{m.Input, m.Output, m.DurationMS} {
		if n != nil && *n < 0 {
			return fmt.Errorf("negative evidence value")
		}
	}
	if (m.Kind == "missing" || m.Coverage == "unavailable") && (m.Input != nil || m.Output != nil) {
		return fmt.Errorf("missing/unavailable usage requires null tokens")
	}
	if m.Event != "" && !contains1([]string{"new_defect", "false_positive", "accepted_fix", "failure", "recovery_started", "recovered", "recovery_failed"}, m.Event) {
		return fmt.Errorf("invalid evidence event")
	}
	if m.Reason != "" && !contains1([]string{"unknown", "timeout", "interrupted", "exit", "scope", "persistence", "capacity", "quota", "validation", "human_confirmed"}, m.Reason) {
		return fmt.Errorf("invalid reason code")
	}
	if m.Related != "" && !evidenceID.MatchString(m.Related) {
		return fmt.Errorf("invalid related evidence ID")
	}
	if contains1([]string{"accepted_fix", "recovered", "recovery_failed"}, m.Event) && m.Related == "" {
		return fmt.Errorf("%s requires related evidence ID", m.Event)
	}
	return nil
}
func measurementPath() string { return filepath.Join(orchHome(), "measurements", "records.jsonl") }
func loadMeasurements() ([]Measurement, error) {
	f, e := os.Open(measurementPath())
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_SH); e != nil {
		return nil, e
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return readMeasurements(f)
}
func recordTaskMeasurement(t *Task) error {
	m := Measurement{Version: 1, ID: t.ID + "-execution", Session: t.Run, Period: "task", Subject: "task", Entity: t.ID, Phase: "execution", Source: "orch-task", Kind: "missing", Coverage: "unavailable", DurationMS: &t.DurationMS}
	m.Model = t.ModelID
	if m.Model == "" {
		m.Model = t.Key
	}
	if t.Usage != nil {
		m.Kind = "measured"
		m.Coverage = "partial"
		m.Input = &t.Usage.InputTokens
		m.Output = &t.Usage.OutputTokens
	}
	if contains1([]string{"failed", "timeout", "interrupted"}, t.Status) {
		m.Event = "failure"
		m.Reason = "exit"
		if t.Status != "failed" {
			m.Reason = t.Status
		}
		if len(t.ScopeViolations) > 0 {
			m.Reason = "scope"
		}
	}
	return recordMeasurement(m)
}

func recordPaneCompletion(runID string, w *Worker, status string) error {
	phase := "execution"
	if strings.HasPrefix(w.Role, "verify-") {
		phase = "review"
	}
	m := Measurement{Version: 1, ID: w.Name + "-completion-" + status, Session: runID, Period: "run", Subject: "pane", Entity: w.Name, Phase: phase, Source: "orch-completion-gate", Kind: "missing", Coverage: "unavailable"}
	m.Model = w.ModelID
	if m.Model == "" {
		m.Model = w.Key
	}
	if status == "failed" || status == "blocked" {
		m.Event = "failure"
		m.Reason = "validation"
	}
	if w.FallbackOf != "" {
		// A fallback may predate explicit measurement. Never invent its start record.
		rows, e := loadMeasurements()
		if e != nil {
			return e
		}
		for _, r := range rows {
			if r.ID == w.Name+"-recovery" && r.Session == runID {
				m.Related = r.ID
				if status == "done" {
					m.Event = "recovered"
				} else {
					m.Event = "recovery_failed"
				}
				m.Phase = "rework"
				break
			}
		}
	}
	return recordMeasurement(m)
}
func recordMeasurement(m Measurement) error {
	if err := m.validate(); err != nil {
		return err
	}
	p := measurementPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	rows, err := readMeasurements(f)
	if err != nil {
		return err
	}
	for _, old := range rows {
		if old.Session == m.Session && old.Period == m.Period && old.ID == m.ID {
			a, b := old, m
			a.At = time.Time{}
			b.At = time.Time{}
			x, _ := json.Marshal(a)
			y, _ := json.Marshal(b)
			if string(x) == string(y) {
				return nil
			}
			return fmt.Errorf("conflicting duplicate evidence %s", m.ID)
		}
	}
	if m.Related != "" {
		found := false
		for _, old := range rows {
			if old.ID == m.Related && old.Session == m.Session {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("related evidence not found: %s", m.Related)
		}
	}
	m.At = time.Now().UTC()
	b, _ := json.Marshal(m)
	if _, err = f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}
func readMeasurements(r io.Reader) ([]Measurement, error) {
	dec := json.NewDecoder(r)
	var rows []Measurement
	for {
		var m Measurement
		err := dec.Decode(&m)
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("measurement log is malformed; preserve and repair: %w", err)
		}
		if err = m.validate(); err != nil {
			return nil, err
		}
		rows = append(rows, m)
	}
}
func measurementCommand(args []string, in io.Reader, out io.Writer) error {
	if len(args) > 0 && args[0] == "collect" {
		return collectUsageCommand(args[1:], out)
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: orch measure import < record.json; orch measure report")
	}
	if args[0] == "import" {
		dec := json.NewDecoder(io.LimitReader(in, 1<<20))
		dec.DisallowUnknownFields()
		var m Measurement
		if err := dec.Decode(&m); err != nil {
			return err
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return fmt.Errorf("one JSON record required")
		}
		return recordMeasurement(m)
	}
	if args[0] != "report" {
		return fmt.Errorf("use measure import or report")
	}
	rows, err := loadMeasurements()
	if err != nil {
		return err
	}
	type group struct {
		Total         int   `json:"n_total"`
		With          int   `json:"n_with_value"`
		WithInput     int   `json:"n_with_input"`
		MissingInput  int   `json:"n_missing_input"`
		WithOutput    int   `json:"n_with_output"`
		MissingOutput int   `json:"n_missing_output"`
		Missing       int   `json:"n_missing"`
		Input         int64 `json:"observed_input_subtotal"`
		Output        int64 `json:"observed_output_subtotal"`
		DurationWith  int   `json:"n_with_duration"`
		DurationMS    int64 `json:"observed_duration_ms_subtotal"`
	}
	groups := map[string]*group{}
	events := map[string]int{}
	for _, m := range rows {
		if m.Event != "" {
			events[m.Event]++
		}
		k := strings.Join([]string{m.Session, m.Period, m.Subject, m.Entity, m.Phase, m.Source, m.Model, m.Kind, m.Coverage}, "|")
		g := groups[k]
		if g == nil {
			g = &group{}
			groups[k] = g
		}
		g.Total++
		if m.DurationMS != nil {
			g.DurationWith++
			g.DurationMS += *m.DurationMS
		}
		if m.Input != nil && m.Output != nil {
			g.With++
		} else {
			g.Missing++
		}
		if m.Input != nil {
			g.WithInput++
			g.Input += *m.Input
		} else {
			g.MissingInput++
		}
		if m.Output != nil {
			g.WithOutput++
			g.Output += *m.Output
		} else {
			g.MissingOutput++
		}
	}
	return json.NewEncoder(out).Encode(map[string]any{"groups": groups, "n_total": len(rows), "events": events})
}
