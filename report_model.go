package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// reportData mirrors the `orch report --json` contract (decisions D2). Field names, types and
// null rules are a fixed interface with the cockpit: do not rename or add fields.
type reportData struct {
	Run        reportRunInfo        `json:"run"`
	KPI        reportKPI            `json:"kpi"`
	Acceptance []reportAcceptance   `json:"acceptance"`
	Timeline   []reportTimelineItem `json:"timeline"`
	ByKey      []reportKeyRow       `json:"by_key"`
	Markdown   string               `json:"markdown"`
	Final      FinalReport          `json:"final"`
}

type reportRunInfo struct {
	ID           string  `json:"id"`
	Mode         string  `json:"mode"`
	Status       string  `json:"status"`
	HeadTerminal string  `json:"head_terminal"`
	Created      string  `json:"created"`
	Ended        *string `json:"ended"`
	Request      string  `json:"request"`
}

type reportKPI struct {
	Items       int `json:"items"`
	Done        int `json:"done"`
	Failed      int `json:"failed"`
	Other       int `json:"other"`
	WallSeconds int `json:"wall_seconds"`
	WorkSeconds int `json:"work_seconds"`
	Retries     int `json:"retries"`
	Fallbacks   int `json:"fallbacks"`
}

type reportAcceptance struct {
	N     int    `json:"n"`
	Text  string `json:"text"`
	State string `json:"state"`
}

type reportTimelineItem struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Role   string `json:"role"`
	Status string `json:"status"`
	StartS *int   `json:"start_s"`
	EndS   *int   `json:"end_s"`
}

type reportKeyRow struct {
	Key     string `json:"key"`
	N       int    `json:"n"`
	Seconds int    `json:"seconds"`
}

// reportRow is one line of the work table; it carries what the JSON timeline does not.
type reportRow struct {
	Name, Kind, Key, Role, Status string
	Seconds                       *int
	Retries                       int
	Summary                       string
}

// reportSections holds the raw run texts and table rows that only the Markdown and HTML use.
type reportSections struct {
	Created   time.Time
	Request   string
	Decisions string
	Findings  string
	Events    []Event
	Rows      []reportRow
	Skipped   int
}

const reportRequestMax = 120

type reportItem struct {
	row      reportRow
	start    *time.Time
	end      *time.Time
	fallback bool
}

// normalizeReportStatus maps worker and task statuses onto the D2 vocabulary.
func normalizeReportStatus(s string) string {
	switch s {
	case "done":
		return "done"
	case "failed", "lost", "content-blocked", "quota", "capacity":
		return "failed"
	case "blocked":
		return "blocked"
	case "partial":
		return "partial"
	case "spawned", "prompted", "running":
		return "running"
	}
	return "other"
}

func truncRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max-1]) + "…"
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

func readReportFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
}

var reportMarkLine = regexp.MustCompile(`^(\[[xX]\]|✓|\[ \])\s*`)

func reportAcceptanceItems(r *Run) []reportAcceptance {
	out := []reportAcceptance{}
	items, err := acceptanceItems(r) // a missing file or an empty list both mean "no criteria"
	if err != nil {
		return out
	}
	for _, it := range items {
		n := 0
		fmt.Sscanf(it[0], "%d", &n)
		text, state := it[1], "unrecorded"
		if m := reportMarkLine.FindString(text); m != "" {
			switch strings.TrimSpace(m) {
			case "[ ]":
				state = "unmet"
			default:
				state = "met"
			}
			text = strings.TrimSpace(strings.TrimPrefix(text, m))
		}
		out = append(out, reportAcceptance{N: n, Text: text, State: state})
	}
	return out
}

func reportWorkerSummary(body string) string {
	s := collectSummary(body)
	s = strings.TrimPrefix(strings.TrimSpace(s), "- ")
	s = strings.TrimSpace(strings.TrimPrefix(s, "summary:"))
	return truncRunes(strings.Join(strings.Fields(s), " "), 160)
}

func workerReportItem(r *Run, w *Worker) reportItem {
	status := normalizeReportStatus(w.Status)
	var body string
	if data, err := os.ReadFile(r.resultPath(w.Name)); err == nil {
		body = string(data)
	}
	if w.Status == "closed" {
		status = "other"
		if st := resultStatus(r.resultPath(w.Name)); st != "" {
			status = st
		}
	}
	it := reportItem{row: reportRow{Name: w.Name, Kind: "pane", Key: w.Key, Role: w.Role, Status: status,
		Retries: w.CapacityRetries + w.BlockRetries, Summary: reportWorkerSummary(body)}, fallback: w.FallbackOf != ""}
	if !w.Started.IsZero() {
		start := w.Started
		it.start = &start
		if !w.Closed.IsZero() && !w.Closed.Before(w.Started) {
			sec := int(w.Closed.Sub(w.Started).Seconds())
			it.row.Seconds = &sec
			if status != "running" {
				end := w.Closed
				it.end = &end
			}
		}
	}
	return it
}

type reportTaskRecord struct {
	Task
	Started  time.Time `json:"started"`
	Purpose  string    `json:"purpose"`
	Duration *int64    `json:"duration_ms"`
}

// taskReportItems lists run.json tasks (decision D12) and enriches each from tasks/<id>/task.json.
// A task whose task.json is missing or unreadable stays in the list as status "other" with
// unknown start and duration; the second return value counts those.
func taskReportItems(r *Run) ([]reportItem, int) {
	var items []reportItem
	unreadable := 0
	for _, t := range r.Tasks {
		if t == nil || t.ID == "" {
			continue
		}
		kind := t.Kind
		if kind != "ask" && kind != "do" && kind != "test" {
			kind = "ask"
		}
		path := filepath.Join(r.dir(), "tasks", t.ID, "task.json")
		it := reportItem{row: reportRow{Name: t.ID, Kind: kind, Key: t.Key, Status: "other"}}
		var rec reportTaskRecord
		b, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(b, &rec) != nil {
			unreadable++
			items = append(items, it)
			continue
		}
		if rec.Kind == "ask" || rec.Kind == "do" || rec.Kind == "test" {
			it.row.Kind = rec.Kind
		}
		if rec.Key != "" {
			it.row.Key = rec.Key
		}
		status := normalizeReportStatus(rec.Status)
		it.row.Status = status
		it.row.Role = rec.Purpose
		it.row.Retries = rec.CapacityRetries + rec.BlockRetries
		it.row.Summary = truncRunes(firstLine(rec.Answer), 120)
		if rec.Duration != nil && *rec.Duration >= 0 && status != "running" {
			sec := int(*rec.Duration / 1000)
			it.row.Seconds = &sec
		}
		switch {
		case !rec.Started.IsZero():
			start := rec.Started
			it.start = &start
		case it.row.Seconds != nil:
			if info, err := os.Stat(path); err == nil {
				start := info.ModTime().Add(-time.Duration(*rec.Duration) * time.Millisecond)
				it.start = &start
			}
		}
		if it.start != nil && rec.Duration != nil && it.row.Seconds != nil {
			end := it.start.Add(time.Duration(*rec.Duration) * time.Millisecond)
			it.end = &end
		}
		items = append(items, it)
	}
	return items, unreadable
}

// reportSeconds is floor(t - created) in whole seconds, never negative (decision D13).
func reportSeconds(created time.Time, t *time.Time) *int {
	if t == nil {
		return nil
	}
	s := max(0, int(math.Floor(t.Sub(created).Seconds())))
	return &s
}

// isRunEndEvent matches only real end events: exactly "run ended" or "gc: ...; run ended" (D18).
func isRunEndEvent(what string) bool {
	return what == "run ended" || (strings.HasPrefix(what, "gc: ") && strings.HasSuffix(what, "; run ended"))
}

// reportEndTime is the last end event; an active run never ends, and a closed run without one
// ends at its last event (decisions D13, D18).
func reportEndTime(r *Run) *time.Time {
	if r.Status == "active" {
		return nil
	}
	for i := len(r.Events) - 1; i >= 0; i-- {
		if isRunEndEvent(r.Events[i].What) {
			at := r.Events[i].At
			return &at
		}
	}
	if r.Status == "closed" && len(r.Events) > 0 {
		at := r.Events[len(r.Events)-1].At
		return &at
	}
	return nil
}

// buildReport collects run data without any model call. now is injected for tests.
func buildReport(r *Run, now time.Time) (reportData, reportSections, error) {
	sec := reportSections{Created: r.Created, Events: r.Events}
	var err error
	for _, f := range []struct {
		name string
		dst  *string
	}{{"request.md", &sec.Request}, {"decisions.md", &sec.Decisions}, {"findings.md", &sec.Findings}} {
		if *f.dst, err = readReportFile(filepath.Join(r.dir(), f.name)); err != nil {
			return reportData{}, sec, fmt.Errorf("read %s: %w", f.name, err)
		}
	}
	items := []reportItem{}
	for _, w := range r.Workers {
		if w != nil {
			items = append(items, workerReportItem(r, w))
		}
	}
	taskItems, unreadable := taskReportItems(r)
	items = append(items, taskItems...)
	sec.Skipped = unreadable
	startS := make([]*int, len(items))
	for i, it := range items {
		startS[i] = reportSeconds(r.Created, it.start)
	}
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool {
		i, j := order[x], order[y]
		switch {
		case startS[i] == nil && startS[j] != nil:
			return false
		case startS[i] != nil && startS[j] == nil:
			return true
		case startS[i] != nil && *startS[i] != *startS[j]:
			return *startS[i] < *startS[j]
		}
		return items[i].row.Name < items[j].row.Name
	})
	sorted := make([]reportItem, len(items))
	sortedStart := make([]*int, len(items))
	for n, i := range order {
		sorted[n], sortedStart[n] = items[i], startS[i]
	}
	items, startS = sorted, sortedStart

	endAt := reportEndTime(r)
	var ended *string
	if endAt != nil {
		s := endAt.Format(time.RFC3339)
		ended = &s
	}
	d := reportData{
		Run: reportRunInfo{ID: r.ID, Mode: r.Mode, Status: r.Status, HeadTerminal: r.HeadPane,
			Created: r.Created.Format(time.RFC3339), Ended: ended, Request: truncRunes(firstLine(sec.Request), reportRequestMax)},
		Acceptance: reportAcceptanceItems(r),
		Timeline:   []reportTimelineItem{},
		ByKey:      []reportKeyRow{},
	}
	end := now
	if endAt != nil {
		end = *endAt
	}
	d.KPI.WallSeconds = max(0, int(end.Sub(r.Created).Seconds()))
	keys := map[string]*reportKeyRow{}
	for idx, it := range items {
		row := it.row
		sec.Rows = append(sec.Rows, row)
		ti := reportTimelineItem{Name: row.Name, Kind: row.Kind, Key: row.Key, Role: row.Role, Status: row.Status,
			StartS: startS[idx]}
		if ti.StartS != nil && row.Status != "running" {
			ti.EndS = reportSeconds(r.Created, it.end)
		}
		d.Timeline = append(d.Timeline, ti)
		d.KPI.Items++
		switch row.Status {
		case "done":
			d.KPI.Done++
		case "failed":
			d.KPI.Failed++
		default:
			d.KPI.Other++
		}
		d.KPI.Retries += row.Retries
		if it.fallback {
			d.KPI.Fallbacks++
		}
		k := keys[row.Key]
		if k == nil {
			k = &reportKeyRow{Key: row.Key}
			keys[row.Key] = k
		}
		k.N++
		if row.Seconds != nil {
			k.Seconds += *row.Seconds
			d.KPI.WorkSeconds += *row.Seconds
		}
	}
	for _, k := range keys {
		d.ByKey = append(d.ByKey, *k)
	}
	sort.Slice(d.ByKey, func(i, j int) bool {
		if d.ByKey[i].Seconds != d.ByKey[j].Seconds {
			return d.ByKey[i].Seconds > d.ByKey[j].Seconds
		}
		return d.ByKey[i].Key < d.ByKey[j].Key
	})
	if d.Final, err = readFinal(r.dir(), d.Run.Request); err != nil {
		return reportData{}, sec, fmt.Errorf("read %s: %w", finalFileName, err)
	}
	d.Markdown = renderReportMarkdown(d, sec)
	return d, sec, nil
}

func hasReportItems(r *Run) bool {
	return len(r.Workers) > 0 || len(r.Tasks) > 0
}
