package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sampleFinal(t *testing.T) FinalReport {
	t.Helper()
	b, err := os.ReadFile("testdata/final_sample.md")
	if err != nil {
		t.Fatal(err)
	}
	return parseFinal(b, "fallback")
}

func TestReportFinalSample(t *testing.T) {
	f := sampleFinal(t)
	if !f.Present || !strings.HasPrefix(f.Title, "orch run 보고서 기능") {
		t.Fatalf("title %q", f.Title)
	}
	parts := strings.Split(f.Conclusion, "\n\n")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "orch report 와 cockpit") || !strings.HasPrefix(parts[1], "Herdr 안에서") {
		t.Fatalf("conclusion %q", f.Conclusion)
	}
	var kinds, titles []string
	for _, b := range f.Blocks {
		kinds = append(kinds, b.Kind)
		titles = append(titles, b.Title)
	}
	if !reflect.DeepEqual(kinds, []string{"list", "table", "risks", "checklist", "text"}) ||
		!reflect.DeepEqual(titles, []string{"한 일", "검증", "남은 위험", "확인할 것", "메모"}) {
		t.Fatalf("blocks %v %v", kinds, titles)
	}
}

func TestReportFinalBlockDetails(t *testing.T) {
	f := sampleFinal(t)
	list, table, risks, check, text := f.Blocks[0], f.Blocks[1], f.Blocks[2], f.Blocks[3], f.Blocks[4]
	if len(list.Items) != 3 || !strings.HasPrefix(list.Items[0].Text, "orch report: 기본은") ||
		!strings.Contains(list.Items[1].Text, "Go 가 SVG 로 그립니다.") {
		t.Fatalf("list %+v", list.Items)
	}
	if !reflect.DeepEqual(table.Header, []string{"단계", "모델", "결과"}) || len(table.Rows) != 3 ||
		table.Rows[1][1] != "codex-high-l" || table.Text != "리뷰는 작성자와 다른 계열이 맡았습니다." {
		t.Fatalf("table %+v", table)
	}
	var levels []string
	for _, it := range risks.Items {
		levels = append(levels, it.Level)
	}
	if !reflect.DeepEqual(levels, []string{"medium", "low", ""}) || !strings.HasPrefix(risks.Items[0].Text, "Hub") {
		t.Fatalf("risks %+v", risks.Items)
	}
	var checked []bool
	for _, it := range check.Items {
		checked = append(checked, it.Checked)
	}
	if !reflect.DeepEqual(checked, []bool{false, true, false}) ||
		check.Items[1].Text != "설정에서 Linear 를 끄면 Report 가 그 자리로 오는지" {
		t.Fatalf("checklist %+v", check.Items)
	}
	if !strings.Contains(text.Text, "*강조*") || !strings.Contains(text.Text, "[링크](https://example.com)") {
		t.Fatalf("text %q", text.Text)
	}
}

func TestReportFinalInlinePlain(t *testing.T) {
	f := parseFinal([]byte("# T\n\n## 결론\n\n**굵게** `코드` [링크](http://x)\n\n## 한 일\n\n- *강조* `a`\n"), "fb")
	if f.Conclusion != "굵게 코드 링크" || f.Blocks[0].Items[0].Text != "강조 a" {
		t.Fatalf("%+v", f)
	}
}

func TestReportFinalMissingAndFallbackTitle(t *testing.T) {
	r, err := readFinal(t.TempDir(), "req")
	if err != nil || r.Present || r.Title != "" || r.Conclusion != "" || r.Blocks == nil || len(r.Blocks) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if f := parseFinal([]byte("## 결론\n\n끝.\n"), "요청 제목"); f.Title != "요청 제목" || !f.Present {
		t.Fatalf("%+v", f)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "final.md"), []byte("## 결론\n\nok\n"), 0600)
	if g, _ := readFinal(dir, "req"); g.Title != "req" {
		t.Fatal(g.Title)
	}
}

func TestReportFinalJSONKeys(t *testing.T) {
	for _, f := range []FinalReport{sampleFinal(t), emptyFinal()} {
		b, _ := json.Marshal(f)
		var m map[string]any
		json.Unmarshal(b, &m)
		for _, k := range []string{"present", "title", "conclusion", "blocks"} {
			if _, ok := m[k]; !ok {
				t.Fatalf("missing %s: %s", k, b)
			}
		}
		for _, raw := range m["blocks"].([]any) {
			blk := raw.(map[string]any)
			for _, k := range []string{"kind", "title", "text", "items", "header", "rows"} {
				if v, ok := blk[k]; !ok || v == nil {
					t.Fatalf("block key %s: %v", k, blk)
				}
			}
			for _, it := range blk["items"].([]any) {
				for _, k := range []string{"text", "checked", "level"} {
					if _, ok := it.(map[string]any)[k]; !ok {
						t.Fatalf("item key %s", k)
					}
				}
			}
		}
	}
}

func TestReportFinalEdgeConsecutiveEmptyHeadings(t *testing.T) {
	f := parseFinal([]byte("# t\n\n##\n\n##\n\n## 정상\n\n본문\n"), "x")
	if len(f.Blocks) != 3 {
		t.Fatalf("blocks = %d, want 3: %+v", len(f.Blocks), f.Blocks)
	}
	if f.Blocks[0].Title != "" || f.Blocks[0].Text != "" || f.Blocks[1].Title != "" || f.Blocks[1].Text != "" {
		t.Errorf("empty blocks wrong: %+v %+v", f.Blocks[0], f.Blocks[1])
	}
	if f.Blocks[2].Title != "정상" || f.Blocks[2].Text != "본문" {
		t.Errorf("normal block wrong: %+v", f.Blocks[2])
	}
}

func TestReportFinalEdgeEmptyHeadingLevelAware(t *testing.T) {
	f := parseFinal([]byte("## a\n\n###\n\n내용\n\n##\n\n끝\n"), "x")
	if len(f.Blocks) != 2 || f.Blocks[1].Title != "" || f.Blocks[1].Text != "끝" {
		t.Fatalf("blocks = %+v", f.Blocks)
	}
}

func TestReportFinalEdgeCRLFMatchesLF(t *testing.T) {
	lf := "# 제목\n\n## 결론\n\n끝\n\n##\n\n##\n\n## 한 일\n\n- a\n- b\n\n## 메모\n\n문단\n"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	a, b := parseFinal([]byte(lf), "x"), parseFinal([]byte(crlf), "x")
	if !reflect.DeepEqual(a, b) {
		t.Errorf("CRLF differs from LF:\n%+v\n%+v", a, b)
	}
	cr := parseFinal([]byte(strings.ReplaceAll(lf, "\n", "\r")), "x")
	if !reflect.DeepEqual(a, cr) {
		t.Errorf("CR differs from LF")
	}
}

func TestReportFinalEdgeEntitiesResolvedOnce(t *testing.T) {
	cases := map[string]string{
		`\&amp;`:    "&amp;",
		`&amp;lt;`:  "&lt;",
		`&#38;amp;`: "&amp;",
		`&amp;`:     "&",
		`\*x\*`:     "*x*",
		`a &lt; b`:  "a < b",
	}
	for in, want := range cases {
		f := parseFinal([]byte("## 결론\n\n"+in+"\n"), "x")
		if f.Conclusion != want {
			t.Errorf("%q -> %q, want %q", in, f.Conclusion, want)
		}
	}
}
