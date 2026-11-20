package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var reportT0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func reportRunFixture(t *testing.T, id, head, status string, created time.Time, adhoc bool, workers int) *Run {
	t.Helper()
	r := &Run{ID: id, HeadPane: head, Status: status, Mode: "full", Created: created, Adhoc: adhoc, Workers: []*Worker{},
		Events: []Event{{At: created, What: "run created"}}}
	for i := 0; i < workers; i++ {
		r.Workers = append(r.Workers, &Worker{Name: fmt.Sprintf("w%d", i), Status: "done", Started: created})
	}
	if err := os.MkdirAll(r.dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func reportPut(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

// reportFullFixture: three workers (done / failed / running), two tasks (ask done / test failed).
func reportFullFixture(t *testing.T, workerName string) *Run {
	t.Helper()
	t.Setenv("ORCH_HOME", t.TempDir())
	at := func(s int) time.Time { return reportT0.Add(time.Duration(s) * time.Second) }
	r := &Run{ID: "run-full", HeadPane: "T1", Status: "active", Mode: "full", Created: reportT0,
		Workers: []*Worker{
			{Name: workerName, Key: "codex-high-m", Role: "impl", Status: "closed", Started: at(10), Closed: at(70), CapacityRetries: 1, BlockRetries: 1},
			{Name: "w-fail", Key: "claude-high-m", Role: "review", Status: "failed", Started: at(20), Closed: at(50), FallbackOf: workerName},
			{Name: "w-run", Key: "claude-high-m", Role: "verify", Status: "running", Started: at(30)},
		},
		Tasks:  []*Task{{ID: "task-1", Kind: "ask"}, {ID: "task-2", Kind: "test"}},
		Events: []Event{{At: reportT0, What: "run created"}, {At: at(150), What: "pipe | and\nnewline"}}}
	if err := os.MkdirAll(r.dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	reportPut(t, r.resultPath(workerName), "# Result\n- status: done\n- summary: built it\n## Deliverables\n")
	reportPut(t, filepath.Join(r.dir(), "request.md"), "first line of request\nsecond line")
	reportPut(t, filepath.Join(r.dir(), "acceptance.md"), "1. [x] met one\n2. [ ] unmet two\n3. plain three\n")
	reportPut(t, filepath.Join(r.dir(), "decisions.md"), "- D1 decision")
	reportPut(t, filepath.Join(r.dir(), "findings.md"), "# Findings\n<script>alert(1)</script>\n")
	reportPut(t, filepath.Join(r.dir(), "tasks", "task-1", "task.json"),
		fmt.Sprintf(`{"task":"task-1","kind":"ask","key":"codex-high-m","status":"done","duration_ms":20000,"answer":"the answer\nmore","started":%q}`, at(5).Format(time.RFC3339)))
	test2 := filepath.Join(r.dir(), "tasks", "task-2", "task.json")
	reportPut(t, test2, `{"task":"task-2","kind":"test","status":"failed","duration_ms":8000,"purpose":"unit"}`)
	if err := os.Chtimes(test2, at(100), at(100)); err != nil {
		t.Fatal(err)
	}
	return r
}

func ip(n int) *int { return &n }

func TestBuildReportKPITimelineByKey(t *testing.T) {
	// Arrange
	r := reportFullFixture(t, "w-done")
	// Act
	d, _, err := buildReport(r, reportT0.Add(200*time.Second))
	// Assert
	if err != nil {
		t.Fatal(err)
	}
	wantKPI := reportKPI{Items: 5, Done: 2, Failed: 2, Other: 1, WallSeconds: 200, WorkSeconds: 118, Retries: 2, Fallbacks: 1}
	if d.KPI != wantKPI {
		t.Fatalf("kpi = %+v, want %+v", d.KPI, wantKPI)
	}
	type row struct {
		name, status string
		start, end   *int
	}
	want := []row{{"task-1", "done", ip(5), ip(25)}, {"w-done", "done", ip(10), ip(70)}, {"w-fail", "failed", ip(20), ip(50)},
		{"w-run", "running", ip(30), nil}, {"task-2", "failed", ip(92), ip(100)}}
	if len(d.Timeline) != len(want) {
		t.Fatalf("timeline has %d items: %+v", len(d.Timeline), d.Timeline)
	}
	eq := func(a, b *int) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
	for i, w := range want {
		g := d.Timeline[i]
		if g.Name != w.name || g.Status != w.status || !eq(g.StartS, w.start) || !eq(g.EndS, w.end) {
			t.Errorf("timeline[%d] = %+v, want %+v", i, g, w)
		}
	}
	wantKeys := []reportKeyRow{{"codex-high-m", 2, 80}, {"claude-high-m", 2, 30}, {"", 1, 8}}
	if fmt.Sprint(d.ByKey) != fmt.Sprint(wantKeys) {
		t.Fatalf("by_key = %v, want %v", d.ByKey, wantKeys)
	}
	if d.Run.Request != "first line of request" || d.Run.Ended != nil {
		t.Fatalf("run info = %+v", d.Run)
	}
}

func TestBuildReportAcceptanceStates(t *testing.T) {
	cases := []struct {
		name, body string
		want       []reportAcceptance
	}{
		{"marks", "1. [x] a\n2. [X] b\n3. [ ] c\n4. ✓ d\n5. e\n", []reportAcceptance{{1, "a", "met"}, {2, "b", "met"}, {3, "c", "unmet"}, {4, "d", "met"}, {5, "e", "unrecorded"}}},
		{"template without items", acceptanceTemplate, []reportAcceptance{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := reportFullFixture(t, "w-done")
			reportPut(t, filepath.Join(r.dir(), "acceptance.md"), c.body)
			d, _, err := buildReport(r, reportT0)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(d.Acceptance) != fmt.Sprint(c.want) || d.Acceptance == nil {
				t.Fatalf("acceptance = %#v, want %#v", d.Acceptance, c.want)
			}
		})
	}
}

func TestReportJSONKeysAreFixed(t *testing.T) {
	r := reportFullFixture(t, "w-done")
	d, _, err := buildReport(r, reportT0.Add(200*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]json.RawMessage) string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	sub := func(name string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(top[name], &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	first := func(name string) map[string]json.RawMessage {
		var l []map[string]json.RawMessage
		if err := json.Unmarshal(top[name], &l); err != nil || len(l) == 0 {
			t.Fatalf("%s: %v", name, err)
		}
		return l[0]
	}
	checks := []struct{ name, got, want string }{
		{"top", keys(top), "acceptance,by_key,final,kpi,markdown,run,timeline"},
		{"run", keys(sub("run")), "created,ended,head_terminal,id,mode,request,status"},
		{"kpi", keys(sub("kpi")), "done,failed,fallbacks,items,other,retries,wall_seconds,work_seconds"},
		{"acceptance", keys(first("acceptance")), "n,state,text"},
		{"timeline", keys(first("timeline")), "end_s,key,kind,name,role,start_s,status"},
		{"by_key", keys(first("by_key")), "key,n,seconds"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s keys = %s, want %s", c.name, c.got, c.want)
		}
	}
	if string(sub("run")["ended"]) != "null" {
		t.Errorf("active run ended = %s, want null", sub("run")["ended"])
	}
	var tl []map[string]json.RawMessage
	_ = json.Unmarshal(top["timeline"], &tl)
	if string(tl[3]["end_s"]) != "null" {
		t.Errorf("running end_s = %s, want null", tl[3]["end_s"])
	}
}

func TestReportEmptyRunHasEmptyArrays(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	r := reportRunFixture(t, "empty", "T1", "active", reportT0, false, 0)
	d, sec, err := buildReport(r, reportT0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	for _, k := range []string{`"acceptance":[]`, `"timeline":[]`, `"by_key":[]`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("json lacks %s: %s", k, raw)
		}
	}
	page, err := renderReportHTML(d, sec)
	if err != nil || !strings.Contains(string(page), "기록된 작업 없음") {
		t.Fatalf("empty html: %v", err)
	}
}

func TestReportHTMLEscapesRunText(t *testing.T) {
	r := reportFullFixture(t, "<b>evil</b>")
	reportPut(t, filepath.Join(r.dir(), "request.md"), "<script>alert(2)</script> ![x](https://example.com/p.png)")
	d, sec, err := buildReport(r, reportT0.Add(200*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	out, err := renderReportHTML(d, sec)
	if err != nil {
		t.Fatal(err)
	}
	page := string(out)
	for _, bad := range []string{"<script", "<b>", "<img", "alert(1)</script>"} {
		if strings.Contains(page, bad) {
			t.Errorf("html contains raw %q", bad)
		}
	}
	if !strings.Contains(page, "&lt;b&gt;evil&lt;/b&gt;") {
		t.Errorf("svg label is not escaped")
	}
	if strings.Contains(string(timelineSVG(d)), "<b>") {
		t.Errorf("timeline svg contains raw tag")
	}
}

func TestReportHTMLHasNoExternalReferences(t *testing.T) {
	r := reportFullFixture(t, "w-done")
	d, sec, _ := buildReport(r, reportT0.Add(200*time.Second))
	out, err := renderReportHTML(d, sec)
	if err != nil {
		t.Fatal(err)
	}
	page := string(out)
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") || strings.Contains(page, "@import") {
		t.Errorf("external reference found")
	}
	if regexp.MustCompile(`url\([^#)]`).MatchString(page) || strings.Contains(page, "<script") || strings.Contains(page, "<img") {
		t.Errorf("external-capable markup found")
	}
	if strings.Contains(page, "wordmark") || !strings.Contains(page, "source: deepeet design system f98b6ac") {
		t.Errorf("logo or design-system source marker wrong")
	}
	if !strings.Contains(page, "prefers-color-scheme: dark") || !strings.Contains(page, "size: A4") {
		t.Errorf("dark mode or A4 rules missing")
	}
}

func TestReportHTMLSectionOrder(t *testing.T) {
	r := reportFullFixture(t, "w-done")
	d, sec, _ := buildReport(r, reportT0.Add(200*time.Second))
	out, _ := renderReportHTMLOpts(d, sec, true)
	page := string(out)
	titles := []string{"<h1>", "주요 지표</h2>", "실행 기록</h2>", "요청</h3>", "수용 기준</h3>", "Head 결정</h3>",
		"작업 타임라인</h3>", ">작업</h3>", "키별 소요</h3>", "검증 결과</h3>", "이벤트</h3>"}
	last := -1
	for _, title := range titles {
		i := strings.Index(page, title)
		if i < 0 || i < last {
			t.Fatalf("section %q missing or out of order (at %d after %d)", title, i, last)
		}
		last = i
	}
	if strings.Count(page, "<svg") != 2 || !strings.Contains(page, `id="hatch"`) {
		t.Errorf("want exactly two inline svg charts with a hatch pattern")
	}
	for _, label := range []string{"완료", "실패", "진행 중"} {
		if !strings.Contains(string(timelineSVG(d)), label) {
			t.Errorf("timeline lacks text label %q", label)
		}
	}
}

func TestSelectReportRun(t *testing.T) {
	cases := []struct {
		name, runID, terminal, want, wantErr string
		build                                func(t *testing.T)
	}{
		{name: "explicit run wins", runID: "a", want: "a", build: func(t *testing.T) {
			reportRunFixture(t, "a", "T1", "closed", reportT0, false, 1)
			reportRunFixture(t, "b", "T1", "active", reportT0.Add(time.Hour), false, 1)
		}},
		{name: "active run of terminal, newest", terminal: "T1", want: "c", build: func(t *testing.T) {
			reportRunFixture(t, "a", "T1", "closed", reportT0.Add(5*time.Hour), false, 1)
			reportRunFixture(t, "b", "T1", "active", reportT0, false, 0)
			reportRunFixture(t, "c", "T1", "active", reportT0.Add(time.Hour), false, 0)
		}},
		{name: "closed run when no active", terminal: "T1", want: "b", build: func(t *testing.T) {
			reportRunFixture(t, "a", "T1", "closed", reportT0, false, 1)
			reportRunFixture(t, "b", "T1", "closed", reportT0.Add(time.Hour), false, 0)
			reportRunFixture(t, "z", "T1", "closed", reportT0.Add(2*time.Hour), true, 1)
		}},
		{name: "fallback to latest run with items", terminal: "T9", want: "b", build: func(t *testing.T) {
			reportRunFixture(t, "a", "T1", "closed", reportT0, false, 1)
			reportRunFixture(t, "b", "T2", "closed", reportT0.Add(time.Hour), false, 2)
			reportRunFixture(t, "c", "T2", "active", reportT0.Add(2*time.Hour), false, 0)
			reportRunFixture(t, "d", "T2", "closed", reportT0.Add(3*time.Hour), true, 1)
		}},
		{name: "no terminal falls back too", want: "a", build: func(t *testing.T) {
			reportRunFixture(t, "a", "T1", "closed", reportT0, false, 1)
		}},
		{name: "nothing found", wantErr: "no run found", build: func(t *testing.T) {
			reportRunFixture(t, "a", "T1", "active", reportT0, false, 0)
		}},
		{name: "both selectors", runID: "a", terminal: "T1", wantErr: "cannot be combined", build: func(t *testing.T) {}},
		{name: "unknown run", runID: "nope", wantErr: "not found", build: func(t *testing.T) {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ORCH_HOME", t.TempDir())
			c.build(t)
			r, err := selectReportRun(c.runID, c.terminal)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil || r.ID != c.want {
				t.Fatalf("got %v, %v; want %s", r, err, c.want)
			}
		})
	}
}

func TestReportCommandOutputs(t *testing.T) {
	r := reportFullFixture(t, "w-done")
	var opened []string
	old := reportOpen
	reportOpen = func(p string) error { opened = append(opened, p); return nil }
	defer func() { reportOpen = old }()
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := reportCommand(args, &out, &errb)
		return code, out.String(), errb.String()
	}

	code, out, _ := run("--run", r.ID, "--full")
	mdPath := filepath.Join(r.dir(), "report.md")
	if body, err := os.ReadFile(mdPath); code != 0 || strings.TrimSpace(out) != mdPath || err != nil || !strings.Contains(string(body), "## 수용 기준") {
		t.Fatalf("report.md: code=%d out=%q err=%v", code, out, err)
	}
	code, out, _ = run("--run", r.ID, "--html", "--open")
	htmlPath := filepath.Join(r.dir(), "report.html")
	if _, err := os.Stat(htmlPath); code != 0 || strings.TrimSpace(out) != htmlPath || err != nil || len(opened) != 1 || opened[0] != htmlPath {
		t.Fatalf("report.html: code=%d out=%q err=%v opened=%v", code, out, err, opened)
	}
	code, out, _ = run("--for-terminal", "T1", "--json")
	var d reportData
	if code != 0 || json.Unmarshal([]byte(out), &d) != nil || d.Run.ID != r.ID {
		t.Fatalf("json: code=%d out=%q", code, out)
	}
	if !strings.Contains(d.Markdown, "## 요청") || strings.Contains(d.Markdown, "주요 지표") {
		t.Errorf("markdown field = %q", d.Markdown)
	}
	for _, bad := range [][]string{{"--json", "--html"}, {"--run", "x", "--for-terminal", "T1"}, {"--open"}} {
		if code, _, stderr := run(bad...); code != 2 || !strings.HasPrefix(stderr, "orch report:") {
			t.Errorf("%v: code=%d stderr=%q", bad, code, stderr)
		}
	}
}

func TestReportCommandNoRun(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := reportCommand(nil, &out, &errb); code != 1 || errb.String() != "orch report: no run found\n" {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
}

func TestReportMarkdownEscapesTableCells(t *testing.T) {
	r := reportFullFixture(t, "w-done")
	d, _, _ := buildReport(r, reportT0.Add(200*time.Second))
	if !strings.Contains(d.Markdown, `pipe \| and newline`) {
		t.Errorf("event cell not escaped: %s", d.Markdown)
	}
}
