package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func finalHTMLPage(t *testing.T, finalMD string) string {
	t.Helper()
	return finalHTMLPageOpts(t, finalMD, false)
}

func finalHTMLPageOpts(t *testing.T, finalMD string, full bool) string {
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
	return string(out)
}

func sampleFinalMD(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/final_sample.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReportHTMLFinalOrder(t *testing.T) {
	page := finalHTMLPageOpts(t, sampleFinalMD(t), true)
	marks := []string{`id="conclusion"`, "주요 지표</h2>", "<h2>한 일</h2>", "<h2>검증</h2>", "<h2>남은 위험</h2>",
		"<h2>확인할 것</h2>", "<h2>메모</h2>", `id="appendix">실행 기록</h2>`, "요청</h3>"}
	last := -1
	for _, m := range marks {
		i := strings.Index(page, m)
		if i < 0 || i < last {
			t.Fatalf("%q missing or out of order (%d after %d)", m, i, last)
		}
		last = i
	}
	if strings.Contains(page, "<details") {
		t.Error("appendix must not use details")
	}
}

func TestReportHTMLFinalComponents(t *testing.T) {
	page := finalHTMLPage(t, sampleFinalMD(t))
	for _, want := range []string{
		`badge st-warn">중간<`, `badge st-neutral">낮음<`, `badge st-neutral">등급 없음<`,
		`class="checks"`, `aria-label="확인함"`, `aria-label="확인 전"`, `class="card-table"`, `class="conclusion card-running"`,
		`<em>강조</em>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Count(page, `aria-label="확인함"`) != 1 || strings.Count(page, `aria-label="확인 전"`) != 2 {
		t.Error("checkbox states wrong")
	}
	high := finalHTMLPage(t, "## 남은 위험\n\n- [높음] 치명\n")
	if !strings.Contains(high, `badge st-failed">높음<`) {
		t.Error("high risk badge class")
	}
}

func TestReportHTMLFinalMissingNotice(t *testing.T) {
	page := finalHTMLPage(t, "")
	if !strings.Contains(page, "Head 최종 보고 없음") || !strings.Contains(page, "진행</h2>") || strings.Contains(page, "실행 기록</h2>") {
		t.Error("missing-final notice or progress section wrong in default report")
	}
	if !strings.Contains(finalHTMLPageOpts(t, "", true), "실행 기록</h2>") {
		t.Error("--full lacks the run record appendix")
	}
	if strings.Contains(finalHTMLPage(t, sampleFinalMD(t)), "Head 최종 보고 없음") {
		t.Error("notice shown although final exists")
	}
}

func TestReportHTMLFinalEscapes(t *testing.T) {
	page := finalHTMLPage(t, "# <script>t</script>\n\n## 결론\n\n<script>alert(1)</script>\n\n## 한 일\n\n- <script>x</script>\n\n## 메모\n\n<script>y</script> text\n")
	if strings.Contains(page, "<script") {
		t.Error("script tag leaked into page")
	}
}

func TestReportHTMLFinalStatusColors(t *testing.T) {
	page := finalHTMLPage(t, "")
	for _, c := range []string{"#116329", "#a40e26", "#0550ae", "#7d4e00", "#57606a", "#3fb950", "#f85149", "#58a6ff", "#d29922", "#8b949e"} {
		if !strings.Contains(page, c) {
			t.Errorf("colour %s missing", c)
		}
	}
	if !strings.Contains(page, `class="st-done"`) || !strings.Contains(page, `class="st-failed"`) {
		t.Error("chart bars lack status colour classes")
	}
}
