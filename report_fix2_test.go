package main

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func finalBlockByTitle(t *testing.T, f FinalReport, title string) FinalBlock {
	t.Helper()
	for _, b := range f.Blocks {
		if b.Title == title {
			return b
		}
	}
	t.Fatalf("block %q not found in %+v", title, f.Blocks)
	return FinalBlock{}
}

// F1: an empty ## heading must not panic and keeps its place as a block with an empty title.
func TestFinalEmptyHeadingNoPanic(t *testing.T) {
	f := parseFinal([]byte("# T\n\n##\n\n본문 하나\n"), "fb")
	if len(f.Blocks) != 1 || f.Blocks[0].Title != "" || f.Blocks[0].Kind != "text" || f.Blocks[0].Text != "본문 하나" {
		t.Fatalf("%+v", f.Blocks)
	}
	g := parseFinal([]byte("## \n\n- a\n\n## 결론\n\n끝\n\n##\n"), "fb")
	if len(g.Blocks) != 2 || g.Blocks[0].Kind != "list" || g.Conclusion != "끝" || g.Blocks[1].Title != "" {
		t.Fatalf("%+v", g)
	}
}

func TestFinalEmptyThenTitledHeading(t *testing.T) {
	f := parseFinal([]byte("##\n## 한 일\n\n- x\n"), "fb")
	if len(f.Blocks) != 2 || f.Blocks[0].Title != "" || f.Blocks[1].Title != "한 일" || len(f.Blocks[1].Items) != 1 {
		t.Fatalf("%+v", f.Blocks)
	}
	h := parseFinal([]byte("#\n\n## 메모\n\n```\n##\n```\n"), "fb")
	if len(h.Blocks) != 1 || !strings.Contains(h.Blocks[0].Text, "##") {
		t.Fatalf("%+v", h.Blocks)
	}
}

// F1: a panic while rendering becomes a warning; the run-end report step never throws.
func TestRunEndReportPanicIsWarning(t *testing.T) {
	r := runEndFixture(t, "direct", false)
	orig := reportRender
	reportRender = func(reportData, reportSections, bool) ([]byte, error) { panic("boom") }
	defer func() { reportRender = orig }()
	var out, errb bytes.Buffer
	writeRunEndReport(r.ID, &out, &errb)
	if !strings.Contains(errb.String(), "orch: warning: report:") || !strings.Contains(errb.String(), "boom") || out.Len() != 0 {
		t.Errorf("out %q err %q", out.String(), errb.String())
	}
}

// F2: a (possibly dangling) symlink named final.md in the run directory is refused.
func TestHeadGuardRejectsFinalSymlink(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "run-1")
	dir := runDir("run-1")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "final.md")
	in := hookJSON("Write", map[string]string{"path": path})
	if got := headExecutionReason(in); got != "" {
		t.Fatalf("plain final.md refused: %s", got)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing", "target.md"), path); err != nil {
		t.Fatal(err)
	}
	if got := headExecutionReason(in); got == "" {
		t.Fatal("dangling symlink final.md allowed")
	}
}

// F4: text blocks keep indentation; only surrounding blank lines go.
func TestFinalTextKeepsIndent(t *testing.T) {
	f := parseFinal([]byte("## 코드\n\n    code\n    more\n\n"), "fb")
	if f.Blocks[0].Kind != "text" || f.Blocks[0].Text != "    code\n    more" {
		t.Fatalf("%q", f.Blocks[0].Text)
	}
}

// F5: checkbox state comes from the task-list AST, not from the plain text.
func TestFinalCheckboxWithReferenceDefinition(t *testing.T) {
	f := parseFinal([]byte("## 확인할 것\n\n- [x] Confirm\n- [ ] Later\n\n[x]: https://example.com\n"), "fb")
	items := f.Blocks[0].Items
	if f.Blocks[0].Kind != "checklist" || len(items) != 2 || !items[0].Checked || items[0].Text != "Confirm" || items[1].Checked {
		t.Fatalf("%+v", f.Blocks[0])
	}
}

// F6/D12: a table block is paragraphs then exactly one table; anything else is text.
func TestFinalTableShapes(t *testing.T) {
	tbl := "| a | b |\n| - | - |\n| 1 | 2 |\n"
	two := parseFinal([]byte("## 두 표\n\n"+tbl+"\n"+tbl), "fb")
	if two.Blocks[0].Kind != "text" {
		t.Errorf("two tables: %s", two.Blocks[0].Kind)
	}
	after := parseFinal([]byte("## 뒤 문단\n\n"+tbl+"\n표 뒤 문단\n"), "fb")
	if after.Blocks[0].Kind != "text" || !strings.Contains(after.Blocks[0].Text, "표 뒤 문단") {
		t.Errorf("paragraph after table: %+v", after.Blocks[0])
	}
	ok := parseFinal([]byte("## 표\n\n앞 문단\n\n"+tbl), "fb")
	if b := ok.Blocks[0]; b.Kind != "table" || b.Text != "앞 문단" || !reflect.DeepEqual(b.Header, []string{"a", "b"}) || len(b.Rows) != 1 {
		t.Errorf("table: %+v", b)
	}
	withList := parseFinal([]byte("## 섞임\n\n"+tbl+"\n- x\n"), "fb")
	if withList.Blocks[0].Kind != "text" {
		t.Errorf("table+list: %s", withList.Blocks[0].Kind)
	}
}

// F7/D12: ordered lists and list+paragraph mixes stay text so numbering and prose survive.
func TestFinalOrderedAndMixedAreText(t *testing.T) {
	f := parseFinal([]byte("## 순서\n\n1. 하나\n2. 둘\n\n## 혼합\n\n- a\n\n문단\n\n## 점\n\n- a\n- b\n"), "fb")
	if f.Blocks[0].Kind != "text" || !strings.Contains(f.Blocks[0].Text, "2. 둘") {
		t.Errorf("ordered: %+v", f.Blocks[0])
	}
	if f.Blocks[1].Kind != "text" || !strings.Contains(f.Blocks[1].Text, "문단") {
		t.Errorf("mixed: %+v", f.Blocks[1])
	}
	if f.Blocks[2].Kind != "list" || len(f.Blocks[2].Items) != 2 {
		t.Errorf("bullets: %+v", f.Blocks[2])
	}
}

// F8: escapes and entities resolve; code spans stay literal.
func TestFinalPlainTextResolvesEscapesAndEntities(t *testing.T) {
	f := parseFinal([]byte("## 결론\n\n\\*별\\* &amp; &#35;1 `a\\*b`\n\n## 한 일\n\n- 1\\. 항목 &lt;x&gt;\n"), "fb")
	if f.Conclusion != "*별* & #1 a\\*b" || f.Blocks[0].Items[0].Text != "1. 항목 <x>" {
		t.Fatalf("%q %+v", f.Conclusion, f.Blocks[0].Items)
	}
}

// F9: code block content stays in the conclusion.
func TestFinalConclusionKeepsCodeBlock(t *testing.T) {
	f := parseFinal([]byte("## 결론\n\n앞 문단\n\n```\ncode\n```\n\n뒤 문단\n"), "fb")
	if f.Conclusion != "앞 문단\n\ncode\n\n뒤 문단" {
		t.Fatalf("%q", f.Conclusion)
	}
}

var statusColour = regexp.MustCompile(`--st-(done|failed|running|warn|neutral): (#[0-9a-f]{6})`)

func parseHex(t *testing.T, h string) [3]float64 {
	t.Helper()
	var c [3]float64
	for i := range c {
		v, err := strconv.ParseUint(h[1+2*i:3+2*i], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		c[i] = float64(v)
	}
	return c
}

func luminance(c [3]float64) float64 {
	var l [3]float64
	for i, v := range c {
		v /= 255
		if v <= 0.03928 {
			l[i] = v / 12.92
		} else {
			l[i] = math.Pow((v+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*l[0] + 0.7152*l[1] + 0.0722*l[2]
}

func contrast(a, b [3]float64) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// F3/D11: every badge colour is >= 4.5:1 on its own 12% tint over the page background.
func TestReportStatusColoursContrast(t *testing.T) {
	css, err := reportFS.ReadFile("templates/report/report.css")
	if err != nil {
		t.Fatal(err)
	}
	found := statusColour.FindAllStringSubmatch(string(css), -1)
	if len(found) != 10 {
		t.Fatalf("expected 5 light + 5 dark colours, got %d", len(found))
	}
	for i, m := range found {
		bg := [3]float64{255, 255, 255}
		if i >= 5 {
			bg = [3]float64{0, 0, 0}
		}
		fg := parseHex(t, m[2])
		var tint [3]float64
		for k := range tint {
			tint[k] = fg[k]*0.12 + bg[k]*0.88
		}
		if r := contrast(fg, tint); r < 4.5 {
			t.Errorf("%s %s: %.2f:1 on tint, want >= 4.5", m[1], m[2], r)
		}
		if r := contrast(fg, bg); r < 4.5 {
			t.Errorf("%s %s: %.2f:1 on background, want >= 4.5", m[1], m[2], r)
		}
	}
}

// N-a/D13: every final block is a card with a title and a kind label.
func TestReportHTMLFinalBlockCards(t *testing.T) {
	page := finalHTMLPage(t, sampleFinalMD(t))
	if n := strings.Count(page, `<section class="final-block card kind-`); n != 5 {
		t.Errorf("card wrappers = %d, want 5", n)
	}
	for _, want := range []string{
		`<span class="kind-label">목록 3개</span>`, `<span class="kind-label">표 3행</span>`,
		`<span class="kind-label">위험 중간 1 · 낮음 1</span>`, `<span class="kind-label">확인 3개 중 1개 완료</span>`,
		`<header class="card-head"><h2>한 일</h2>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q", want)
		}
	}
	empty := finalHTMLPage(t, "##\n\n본문\n")
	if !strings.Contains(empty, "<h2>"+untitledBlock+"</h2>") {
		t.Error("empty heading card has no placeholder title")
	}
}

// N-b/D13: 8 KPI tiles use 4 columns.
func TestReportHTMLKPIColumns(t *testing.T) {
	page := finalHTMLPageOpts(t, "", true)
	if !strings.Contains(page, `class="kpis kpis-4"`) || strings.Count(page, "<dt>") < 8 {
		t.Error("KPI grid is not 4 columns")
	}
	for n, want := range map[int]int{8: 4, 6: 3, 9: 3, 12: 4, 5: 4} {
		if got := kpiColumns(n); got != want {
			t.Errorf("kpiColumns(%d) = %d, want %d", n, got, want)
		}
	}
}
