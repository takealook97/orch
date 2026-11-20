package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var conciseAppendixMarks = []string{"실행 기록", "요청</h3>", "수용 기준", "Head 결정", "작업 타임라인", "키별 소요", "검증 결과", "이벤트</h3>"}

func conciseRender(t *testing.T, finalMD string, full bool) (reportData, reportSections, string) {
	t.Helper()
	r := reportFullFixture(t, "w-done")
	if finalMD != "" {
		reportPut(t, filepath.Join(r.dir(), "final.md"), finalMD)
	}
	d, sec, err := buildReport(r, reportT0.Add(200*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	out, err := renderReportHTMLOpts(d, sec, full)
	if err != nil {
		t.Fatal(err)
	}
	return d, sec, string(out)
}

func TestReportConciseHTMLHasNoRunRecord(t *testing.T) {
	_, _, page := conciseRender(t, sampleFinalMD(t), false)
	for _, m := range conciseAppendixMarks {
		if strings.Contains(page, m) {
			t.Errorf("default HTML contains %q", m)
		}
	}
	if strings.Contains(page, `class="appendix"`) || strings.Contains(page, "<svg") {
		t.Error("default HTML with final must have no appendix or chart")
	}
	c, k := strings.Index(page, `id="conclusion"`), strings.Index(page, "주요 지표</h2>")
	if c < 0 || k < 0 || c > k {
		t.Errorf("conclusion card must precede KPI (%d, %d)", c, k)
	}
	if !strings.Contains(page, `class="kpis kpis-3"`) || strings.Count(page, "<dt>") != 3+2 {
		t.Error("default KPI row must have three tiles")
	}
}

func TestReportConciseFullAddsAppendix(t *testing.T) {
	_, _, page := conciseRender(t, sampleFinalMD(t), true)
	for _, m := range []string{"실행 기록</h2>", "요청</h3>", "수용 기준</h3>", "Head 결정</h3>", "작업 타임라인</h3>", "키별 소요</h3>", "검증 결과</h3>", "이벤트</h3>"} {
		if !strings.Contains(page, m) {
			t.Errorf("--full HTML lacks %q", m)
		}
	}
}

func TestReportConciseNoFinalShowsOneTimeline(t *testing.T) {
	_, _, page := conciseRender(t, "", false)
	if !strings.Contains(page, "Head 최종 보고 없음") {
		t.Error("notice missing")
	}
	if strings.Count(page, "<svg") != 1 || !strings.Contains(page, "진행</h2>") {
		t.Errorf("want exactly one timeline svg, got %d", strings.Count(page, "<svg"))
	}
	for _, m := range conciseAppendixMarks {
		if m != "작업 타임라인" && strings.Contains(page, m) { // the chart's own aria label
			t.Errorf("no-final default contains %q", m)
		}
	}
	if strings.Contains(page, "<table") {
		t.Error("work table must not appear")
	}
}

func TestReportConciseMarkdownDefaultAndFull(t *testing.T) {
	d, sec, _ := conciseRender(t, sampleFinalMD(t), false)
	def := renderReportFile(d, sec, false)
	for _, m := range []string{"## 요청", "## 수용 기준", "## Head 결정", "## 작업", "## 이벤트", "실행 기록"} {
		if strings.Contains(def, m) {
			t.Errorf("default report.md contains %q", m)
		}
	}
	if !strings.Contains(def, "## 결론") || !strings.Contains(def, "## 한 일") {
		t.Errorf("default report.md lacks final content:\n%s", def)
	}
	full := renderReportFile(d, sec, true)
	for _, m := range []string{"## 요청", "## 수용 기준", "## 이벤트", "# 실행 기록"} {
		if !strings.Contains(full, m) {
			t.Errorf("--full report.md lacks %q", m)
		}
	}
	d2, sec2, _ := conciseRender(t, "", false)
	nf := renderReportFile(d2, sec2, false)
	if !strings.Contains(nf, "Head 최종 보고 없음") || strings.Contains(nf, "## 요청") {
		t.Errorf("no-final report.md wrong:\n%s", nf)
	}
}

func TestReportConciseJSONKeysUnchanged(t *testing.T) {
	r := reportFullFixture(t, "w-done")
	run := func(args ...string) string {
		var out, errb bytes.Buffer
		if code := reportCommand(args, &out, &errb); code != 0 {
			t.Fatalf("code %d: %s", code, errb.String())
		}
		return out.String()
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(run("--run", r.ID, "--json")), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"run", "kpi", "acceptance", "timeline", "by_key", "markdown", "final"} {
		if _, ok := m[k]; !ok {
			t.Errorf("json lacks %q", k)
		}
	}
	if len(m) != 7 {
		t.Errorf("json has %d keys, want 7", len(m))
	}
	var md string
	_ = json.Unmarshal(m["markdown"], &md)
	if !strings.Contains(md, "## 요청") {
		t.Error("markdown field must stay the run record")
	}
	run("--run", r.ID)
	if b, _ := os.ReadFile(filepath.Join(r.dir(), "report.md")); strings.Contains(string(b), "## 요청") {
		t.Error("default report.md must not hold the run record")
	}
}
