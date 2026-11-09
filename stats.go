package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"
)

type statsRow struct {
	Kind            string   `json:"kind"`
	Key             string   `json:"key"`
	Purpose         string   `json:"purpose"`
	N               int      `json:"n"`
	Done            int      `json:"done"`
	Failed          int      `json:"failed"`
	TestFailures    *int     `json:"test_failures"`
	ScopeViolations *int     `json:"scope_violations"`
	Retries         *int     `json:"retries"`
	Fallbacks       *int     `json:"fallbacks"`
	MeanSeconds     *float64 `json:"mean_seconds"`
	seconds         float64
	durations       int
}
type statsReport struct {
	Rows    []*statsRow `json:"rows"`
	Skipped int         `json:"skipped"`
}

// statsCommand only reads records. Older task schemas have no start timestamp: use
// run creation time, or task file mtime when run metadata is unavailable.
func statsCommand(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(out)
	since := fs.Duration("since", 168*time.Hour, "look back from now")
	asJSON := fs.Bool("json", false, "JSON report")
	pos, err := parse(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 0 || *since < 0 {
		fmt.Fprintln(out, "stats: expected --since nonnegative-duration and/or --json")
		return 2
	}
	report, err := collectStats(time.Now().Add(-*since))
	if err != nil {
		fmt.Fprintln(out, "stats:", err)
		return 1
	}
	if *asJSON {
		if err = json.NewEncoder(out).Encode(report); err != nil {
			return 1
		}
		return 0
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "kind\tkey\tpurpose\tn\tdone\tfailed\ttest_failures\tscope_violations\tretries\tfallbacks\tmean_seconds")
	number := func(n *int) string {
		if n == nil {
			return "-"
		}
		return strconv.Itoa(*n)
	}
	label := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	for _, r := range report.Rows {
		mean := "-"
		if r.MeanSeconds != nil {
			mean = fmt.Sprintf("%.1f", *r.MeanSeconds)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\n", r.Kind, label(r.Key), label(r.Purpose), r.N, r.Done, r.Failed, number(r.TestFailures), number(r.ScopeViolations), number(r.Retries), number(r.Fallbacks), mean)
	}
	if err = tw.Flush(); err != nil {
		return 1
	}
	fmt.Fprintf(out, "skipped=%d\n", report.Skipped)
	return 0
}

func collectStats(cutoff time.Time) (statsReport, error) {
	report := statsReport{Rows: []*statsRow{}}
	entries, err := os.ReadDir(runsDir())
	if os.IsNotExist(err) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	groups := map[[3]string]*statsRow{}
	row := func(kind, key, purpose, status string) *statsRow {
		id := [3]string{kind, key, purpose}
		r := groups[id]
		if r == nil {
			r = &statsRow{Kind: kind, Key: key, Purpose: purpose}
			groups[id] = r
		}
		r.N++
		switch status {
		case "done":
			r.Done++
		case "failed", "timeout", "blocked", "partial", "quota", "capacity", "content-blocked", "lost":
			r.Failed++
		}
		return r
	}
	add := func(target **int, n int) {
		if *target == nil {
			*target = new(int)
		}
		**target += n
	}
	duration := func(r *statsRow, seconds float64) { r.seconds += seconds; r.durations++ }
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(runsDir(), entry.Name())
		var run Run
		data, e := os.ReadFile(filepath.Join(dir, "run.json"))
		if e != nil || json.Unmarshal(data, &run) != nil {
			report.Skipped++
			run = Run{} // Do not count fields partially decoded from a corrupt record.
		}
		for _, w := range run.Workers {
			if w == nil {
				report.Skipped++
				continue
			}
			at := w.Started
			if at.IsZero() {
				at = run.Created
			}
			if at.IsZero() {
				report.Skipped++
				continue
			}
			if at.Before(cutoff) {
				continue
			}
			r := row("pane", w.Key, w.Purpose, w.Status)
			add(&r.Retries, w.CapacityRetries+w.BlockRetries)
			fallback := 0
			if w.FallbackOf != "" {
				fallback = 1
			}
			add(&r.Fallbacks, fallback)
			if !w.Closed.IsZero() && !w.Started.IsZero() && !w.Closed.Before(w.Started) {
				duration(r, w.Closed.Sub(w.Started).Seconds())
			}
		}
		taskDirs, e := os.ReadDir(filepath.Join(dir, "tasks"))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return report, e
		}
		for _, td := range taskDirs {
			if !td.IsDir() {
				continue
			}
			taskPath := filepath.Join(dir, "tasks", td.Name(), "task.json")
			b, e := os.ReadFile(taskPath)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				report.Skipped++
				continue
			}
			var rec struct {
				Task
				Started  time.Time `json:"started"`
				Purpose  string    `json:"purpose"`
				Duration *int64    `json:"duration_ms"`
			}
			if json.Unmarshal(b, &rec) != nil || (rec.Kind != "ask" && rec.Kind != "do" && rec.Kind != "test") {
				report.Skipped++
				continue
			}
			at := rec.Started
			if at.IsZero() {
				at = run.Created
			}
			if at.IsZero() {
				info, err := os.Stat(taskPath)
				if err != nil {
					report.Skipped++
					continue
				}
				at = info.ModTime()
			}
			if at.Before(cutoff) {
				continue
			}
			r := row(rec.Kind, rec.Key, rec.Purpose, rec.Status)
			testFailed := 0
			if rec.Kind == "test" && rec.ExitCode != 0 || rec.Test != nil && rec.Test.ExitCode != 0 {
				testFailed = 1
			}
			add(&r.TestFailures, testFailed)
			scope := 0
			if len(rec.ScopeViolations) > 0 {
				scope = 1
			}
			add(&r.ScopeViolations, scope)
			add(&r.Retries, rec.CapacityRetries+rec.BlockRetries)
			// One-off tasks do not record fallback transitions.
			if rec.Duration != nil && *rec.Duration >= 0 && rec.Status != "running" {
				duration(r, float64(*rec.Duration)/1000)
			}
		}
	}
	for _, r := range groups {
		if r.durations > 0 {
			mean := r.seconds / float64(r.durations)
			r.MeanSeconds = &mean
		}
		report.Rows = append(report.Rows, r)
	}
	sort.Slice(report.Rows, func(i, j int) bool {
		a, b := report.Rows[i], report.Rows[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.N != b.N {
			return a.N > b.N
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Purpose < b.Purpose
	})
	return report, nil
}
