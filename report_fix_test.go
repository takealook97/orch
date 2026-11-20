package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reportTimelineByName(d reportData, name string) (reportTimelineItem, bool) {
	for _, t := range d.Timeline {
		if t.Name == name {
			return t, true
		}
	}
	return reportTimelineItem{}, false
}

// O1: tasks come from run.json; a missing or broken task.json keeps the item as "other".
func TestReportKeepsTasksWithMissingOrBrokenRecord(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	r := &Run{ID: "run-o1", Status: "active", Mode: "full", Created: reportT0,
		Tasks:  []*Task{{ID: "task-1", Kind: "ask", Key: "k"}, {ID: "task-2", Kind: "do", Key: "k"}},
		Events: []Event{{At: reportT0, What: "run created"}}}
	reportPut(t, filepath.Join(r.dir(), "tasks", "task-2", "task.json"), "{not json")
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	d, sec, err := buildReport(r, reportT0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if d.KPI.Items != 2 || d.KPI.Other != 2 || len(d.Timeline) != 2 || sec.Skipped != 2 {
		t.Fatalf("kpi=%+v timeline=%d skipped=%d", d.KPI, len(d.Timeline), sec.Skipped)
	}
	for _, tl := range d.Timeline {
		if tl.Status != "other" || tl.StartS != nil || tl.EndS != nil {
			t.Errorf("%s: status=%s start=%v end=%v", tl.Name, tl.Status, tl.StartS, tl.EndS)
		}
	}
}

// O2: an empty tasks directory is not work; the fallback picks the earlier run with workers.
func TestSelectReportRunSkipsRunWithOnlyEmptyTasksDir(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	reportRunFixture(t, "older", "T9", "closed", reportT0, false, 1)
	newer := reportRunFixture(t, "newer", "T9", "closed", reportT0.Add(time.Hour), false, 0)
	if err := os.MkdirAll(filepath.Join(newer.dir(), "tasks", "task-9"), 0700); err != nil {
		t.Fatal(err)
	}
	r, err := selectReportRun("", "")
	if err != nil || r.ID != "older" {
		t.Fatalf("got %v, %v", r, err)
	}
}

// O3: start_s and end_s are each floored from absolute times, and ties sort by integer start_s.
func TestReportTimelineFloorsAndSortsByIntegerStart(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	ms := func(n int) time.Time { return reportT0.Add(time.Duration(n) * time.Millisecond) }
	r := &Run{ID: "run-o3", Status: "active", Mode: "full", Created: reportT0,
		Workers: []*Worker{
			{Name: "z-first", Status: "closed", Started: ms(200), Closed: ms(1300)},
			{Name: "a-second", Status: "closed", Started: ms(300), Closed: ms(1300)},
		},
		Events: []Event{{At: reportT0, What: "run created"}}}
	if err := os.MkdirAll(r.dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	d, _, err := buildReport(r, reportT0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if d.Timeline[0].Name != "a-second" || d.Timeline[1].Name != "z-first" {
		t.Fatalf("order: %s, %s", d.Timeline[0].Name, d.Timeline[1].Name)
	}
	for _, tl := range d.Timeline {
		if tl.StartS == nil || *tl.StartS != 0 || tl.EndS == nil || *tl.EndS != 1 {
			t.Errorf("%s: start=%v end=%v", tl.Name, tl.StartS, tl.EndS)
		}
	}
}

// O4: ended is the last "run ended" event, not the last event.
func TestReportEndedUsesRunEndedEvent(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	at := func(s int) time.Time { return reportT0.Add(time.Duration(s) * time.Second) }
	r := &Run{ID: "run-o4", Status: "closed", Mode: "full", Created: reportT0,
		Events: []Event{{At: reportT0, What: "run created"}, {At: at(20), What: "run ended"}, {At: at(40), What: "gc: cleanup"}}}
	if err := os.MkdirAll(r.dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	d, _, err := buildReport(r, at(500))
	if err != nil {
		t.Fatal(err)
	}
	if d.Run.Ended == nil || *d.Run.Ended != at(20).Format(time.RFC3339) || d.KPI.WallSeconds != 20 {
		t.Fatalf("ended=%v wall=%d", d.Run.Ended, d.KPI.WallSeconds)
	}
	r.Status, r.Events = "active", r.Events[:1]
	d, _, _ = buildReport(r, at(90))
	if d.Run.Ended != nil || d.KPI.WallSeconds != 90 {
		t.Fatalf("active: ended=%v wall=%d", d.Run.Ended, d.KPI.WallSeconds)
	}
}

// O5: user text in table cells cannot break the table or inject HTML.
func TestMdCellEscapesMetacharacters(t *testing.T) {
	got := mdCell("a|b *x* _y_ `c` [l] <b>\nz\\")
	for _, raw := range []string{"a|b", " *x", "<b>", "\n"} {
		if strings.Contains(got, raw) {
			t.Errorf("%q still contains %q", got, raw)
		}
	}
	if want := `a\|b \*x\* \_y\_ \` + "`c\\`" + ` \[l\] \<b\> z\\`; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestReportHTMLTaskTableEscapesName(t *testing.T) {
	r := reportFullFixture(t, "w-done")
	r.Workers[0].Name = "<b>name</b>"
	d, sec, err := buildReport(r, reportT0.Add(200*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	page, err := renderReportHTML(d, sec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "&lt;b&gt;name&lt;/b&gt;") || strings.Contains(string(page), "<b>name") {
		t.Errorf("work table name not escaped")
	}
}

// O7: the time axis never exceeds 8 ticks.
func TestTickStepLimitsTicks(t *testing.T) {
	for _, span := range []int{1, 300, 3 * 3600, 688 * 86400} {
		step := tickStep(span)
		if ticks := span/step + 1; ticks > 8 {
			t.Errorf("span %d: step %d gives %d ticks", span, step, ticks)
		}
	}
	if s := fmt.Sprint(tickStep(300)); s != "60" {
		t.Errorf("5 min step = %s", s)
	}
}

// D18: only exact end events count; active runs never end.
func TestReportEndTimeStrictEndEvents(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	at := func(s int) time.Time { return reportT0.Add(time.Duration(s) * time.Second) }
	mode := Event{At: at(40), What: "mode light → full: previous run ended elsewhere"}
	build := func(status string, ev ...Event) reportData {
		r := &Run{ID: "run-d18-" + status, Status: status, Mode: "full", Created: reportT0, Events: ev}
		if err := os.MkdirAll(r.dir(), 0700); err != nil {
			t.Fatal(err)
		}
		if err := saveRun(r); err != nil {
			t.Fatal(err)
		}
		d, _, err := buildReport(r, at(500))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	d := build("active", Event{At: reportT0, What: "run created"}, mode)
	if d.Run.Ended != nil {
		t.Fatalf("active ended=%v", *d.Run.Ended)
	}
	d = build("closed", Event{At: reportT0, What: "run created"}, Event{At: at(20), What: "run ended"}, mode)
	if d.Run.Ended == nil || *d.Run.Ended != at(20).Format(time.RFC3339) || d.KPI.WallSeconds != 20 {
		t.Fatalf("closed: ended=%v wall=%d", d.Run.Ended, d.KPI.WallSeconds)
	}
	d = build("closed", Event{At: reportT0, What: "run created"}, Event{At: at(30), What: "gc: Head pane X disappeared; run ended"})
	if d.Run.Ended == nil || *d.Run.Ended != at(30).Format(time.RFC3339) || d.KPI.WallSeconds != 30 {
		t.Fatalf("gc: ended=%v wall=%d", d.Run.Ended, d.KPI.WallSeconds)
	}
}
