package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func fixOrchFinal() FinalReport {
	return FinalReport{
		Present: true, Title: "T &amp;copy;", Conclusion: "&copy; <img src=x>\n\n# 줄 앞 제목\n\n# 제목 아님\n\n- 목록 아님\n\n1. 번호 아님",
		Blocks: []FinalBlock{
			{Kind: "text", Title: "본문", Text: "&amp;copy; &lt;img src=x&gt;"},
			{Kind: "list", Title: "목록", Items: []FinalItem{{Text: "*별* [a](b)"}, {Text: "# 샵"}}},
			{Kind: "table", Title: "표", Header: []string{"h"}, Rows: [][]string{{"&copy;"}}},
		},
	}
}

func TestReportConcisePlainTextSurvivesMarkdown(t *testing.T) {
	md := renderReportFile(reportData{Final: fixOrchFinal()}, reportSections{}, false)
	html, err := markdownHTML(md)
	if err != nil {
		t.Fatal(err)
	}
	got := string(html)
	for _, want := range []string{"&amp;amp;copy;", "&amp;copy;", "&lt;img src=x&gt;", "*별* [a](b)", "# 제목 아님", "- 목록 아님", "1. 번호 아님", "# 줄 앞 제목", "# 샵"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered report.md lost %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"©", "<img", "<em>", "<a href", "<ul>\n<li>목록 아님", "<h1>제목 아님", "<ol>"} {
		if strings.Contains(got, bad) {
			t.Errorf("rendered report.md reinterpreted text as %q", bad)
		}
	}
}

func TestReportConciseMarkdownOrderConclusionBeforeKPI(t *testing.T) {
	md := renderReportFile(reportData{Final: parseFinal([]byte("# T\n\n## 결론\n\nC.\n"), "")}, reportSections{}, false)
	c, k := strings.Index(md, "## 결론"), strings.Index(md, "- 작업")
	if c < 0 || k < 0 || k < c {
		t.Errorf("conclusion must precede KPI line:\n%s", md)
	}
}

func TestReportConcisePrebodyKPIAbsentFromHeader(t *testing.T) {
	md := renderReportFile(reportData{}, reportSections{}, false)
	if i := strings.Index(md, "## 결론"); strings.Contains(md[:i], "작업") {
		t.Errorf("KPI leaked into header:\n%s", md)
	}
}

func fixOrchRows(n int) reportSections {
	sec := reportSections{}
	for i := 0; i < n; i++ {
		s := 61
		sec.Rows = append(sec.Rows, reportRow{Name: fmt.Sprintf("w%02d", i), Status: "done", Seconds: &s})
	}
	return sec
}

func TestReportConcisePlainTextProgressWithoutFinal(t *testing.T) {
	md := renderReportFile(reportData{}, fixOrchRows(3), false)
	if !strings.Contains(md, "## 진행") || !strings.Contains(md, "- w00 · 완료 · 1m01s") || strings.Contains(md, "외 ") {
		t.Errorf("progress section wrong:\n%s", md)
	}
	md = renderReportFile(reportData{}, fixOrchRows(21), false)
	if !strings.Contains(md, "- w19 ·") || strings.Contains(md, "w20") || !strings.Contains(md, "- 외 1건") {
		t.Errorf("21 rows must cap at 20 plus 외 1건:\n%s", md)
	}
}

func TestReportConciseFullBodyIdenticalToDefault(t *testing.T) {
	cases := map[string]reportData{
		"final": {Final: fixOrchFinal(), Markdown: "## 요청\n\nx\n"},
		"none":  {Markdown: "## 요청\n\nx\n"},
	}
	for name, d := range cases {
		sec := fixOrchRows(2)
		def, err := renderReportHTMLOpts(d, sec, false)
		if err != nil {
			t.Fatal(err)
		}
		full, err := renderReportHTMLOpts(d, sec, true)
		if err != nil {
			t.Fatal(err)
		}
		di, fi := strings.Index(string(def), `<footer class="document-footer">`), strings.Index(string(full), `<section class="appendix"`)
		if di < 0 || fi < 0 || string(def[:di]) != string(full[:fi]) {
			t.Errorf("%s: HTML body before appendix differs from default", name)
		}
		if n := strings.Count(string(def[:di]), "<dt>"); n != 2+3 {
			t.Errorf("%s: default dt=%d want 5 (2 meta + 3 KPI)", name, n)
		}
		mdDef := renderReportFile(d, sec, false)
		mdFull := renderReportFile(d, sec, true)
		if !strings.HasPrefix(mdFull, strings.TrimRight(mdDef, "\n")+"\n") || !strings.Contains(mdFull, "# 실행 기록") {
			t.Errorf("%s: report.md --full must start with the default body", name)
		}
	}
}

func TestRunEndReportLongCROnly(t *testing.T) {
	r := runEndFixture(t, "full", false)
	reportPut(t, filepath.Join(r.dir(), "final.md"), strings.Repeat("a\r", 81))
	var out, errb strings.Builder
	writeRunEndReport(r.ID, &out, &errb)
	if !strings.Contains(errb.String(), "final.md is 81 lines") {
		t.Errorf("CR-only 81 lines did not warn: %q", errb.String())
	}
}

func TestReportConciseTextBlockKeepsFencedCode(t *testing.T) {
	final := parseFinal([]byte("# Run\n\n## 결론\n\nDone.\n\n## Example\n\n~~~yaml\nouter:\n  inner: true\n~~~\n"), "")
	if len(final.Blocks) != 1 || !strings.Contains(final.Blocks[0].Text, "\n  inner: true") {
		t.Fatalf("parse lost code before rendering: %+v", final.Blocks)
	}
	html, err := markdownHTML(renderReportFile(reportData{Final: final}, reportSections{}, false))
	if err != nil {
		t.Fatal(err)
	}
	want := "<pre><code class=\"language-yaml\">outer:\n  inner: true\n</code></pre>"
	if !strings.Contains(string(html), want) {
		t.Errorf("fenced YAML payload changed: %s", html)
	}
}
