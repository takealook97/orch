package main

import (
	"fmt"
	"strings"
)

var reportStatusLabel = map[string]string{
	"done": "완료", "failed": "실패", "blocked": "막힘", "partial": "부분 완료", "running": "진행 중", "other": "기타",
}

var reportStateLabel = map[string]string{"met": "충족", "unmet": "미충족", "unrecorded": "미기록"}

type reportSection struct{ Title, Body string }

var mdCellEscaper = strings.NewReplacer("&", "&amp;", `\`, `\\`, "|", `\|`, "`", "\\`", "*", `\*`, "_", `\_`,
	"[", `\[`, "]", `\]`, "<", `\<`, ">", `\>`)

// mdCell makes text safe for one GFM table cell (decision D14): newlines become spaces and
// Markdown and HTML metacharacters are backslash-escaped.
func mdCell(s string) string {
	return mdCellEscaper.Replace(strings.Join(strings.Fields(s), " "))
}

var mdInlineEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `\`, `\\`,
	"`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`)

// mdInline escapes parsed plain text so goldmark renders it back unchanged (decision D10).
func mdInline(s string) string { return mdInlineEscaper.Replace(s) }

// mdLineStart backslash-escapes a block marker at the start of one already inline-escaped line.
func mdLineStart(line string) string {
	t := strings.TrimLeft(line, " \t")
	if t == "" {
		return ""
	}
	switch t[0] {
	case '#', '-', '+', '=', '~':
		return `\` + t
	}
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i > 0 && i < len(t) && (t[i] == '.' || t[i] == ')') {
		return t[:i] + `\` + t[i:]
	}
	return t
}

// mdText escapes multi-line plain text: inline metacharacters first, then per-line block markers.
func mdText(s string) string {
	lines := strings.Split(mdInline(s), "\n")
	for i, l := range lines {
		lines[i] = mdLineStart(l)
	}
	return strings.Join(lines, "\n")
}

// mdHeading escapes a title for one heading line; newlines collapse to spaces.
func mdHeading(s string) string { return mdInline(strings.Join(strings.Fields(s), " ")) }

func reportDuration(sec int) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
	}
	return fmt.Sprintf("%dh%02dm", sec/3600, sec%3600/60)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func rawOrNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(없음)"
	}
	return strings.TrimSpace(s)
}

// reportSectionBodies returns D5 sections 3, 4, 5, 7, 9 and 10 as Markdown.
func reportSectionBodies(d reportData, sec reportSections) []reportSection {
	var acc strings.Builder
	if len(d.Acceptance) == 0 {
		acc.WriteString("(수용 기준 없음)")
	} else {
		acc.WriteString("| 번호 | 기준 | 상태 |\n| --- | --- | --- |\n")
		for _, a := range d.Acceptance {
			fmt.Fprintf(&acc, "| %d | %s | %s |\n", a.N, mdCell(a.Text), reportStateLabel[a.State])
		}
	}
	var work strings.Builder
	if len(sec.Rows) == 0 {
		work.WriteString("(기록된 작업 없음)")
	} else {
		work.WriteString("| 이름 | 종류 | 키 | 역할 | 상태 | 소요 | 재시도 | 요약 |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, r := range sec.Rows {
			dur := "-"
			if r.Seconds != nil {
				dur = reportDuration(*r.Seconds)
			}
			fmt.Fprintf(&work, "| %s | %s | %s | %s | %s | %s | %d | %s |\n", mdCell(r.Name), mdCell(r.Kind), mdCell(orDash(r.Key)),
				mdCell(orDash(r.Role)), reportStatusLabel[r.Status], dur, r.Retries, mdCell(orDash(r.Summary)))
		}
	}
	var ev strings.Builder
	if len(sec.Events) == 0 {
		ev.WriteString("(이벤트 없음)")
	} else {
		ev.WriteString("| 시각 | 내용 |\n| --- | --- |\n")
		for _, e := range sec.Events {
			fmt.Fprintf(&ev, "| %s | %s |\n", e.At.Format("2006-01-02 15:04:05"), mdCell(e.What))
		}
	}
	return []reportSection{
		{"요청", rawOrNone(sec.Request)},
		{"수용 기준", strings.TrimRight(acc.String(), "\n")},
		{"Head 결정", rawOrNone(sec.Decisions)},
		{"작업", strings.TrimRight(work.String(), "\n")},
		{"검증 결과", rawOrNone(sec.Findings)},
		{"이벤트", strings.TrimRight(ev.String(), "\n")},
	}
}

// renderReportMarkdown is the JSON `markdown` field: the report without chart and KPI blocks.
func renderReportMarkdown(d reportData, sec reportSections) string {
	var b strings.Builder
	for _, s := range reportSectionBodies(d, sec) {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", s.Title, s.Body)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// finalBlocksMarkdown renders the parsed final blocks back to Markdown (document order).
func finalBlocksMarkdown(blocks []FinalBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		title := mdHeading(blk.Title)
		if title == "" {
			title = mdHeading(untitledBlock)
		}
		fmt.Fprintf(&b, "## %s\n\n", title)
		switch blk.Kind {
		case "text":
			if blk.Text != "" {
				// kind=text is Markdown source: keep it verbatim (fences, indentation).
				b.WriteString(blk.Text + "\n\n")
			}
			continue
		}
		if blk.Text != "" {
			b.WriteString(mdText(blk.Text) + "\n\n")
		}
		switch blk.Kind {
		case "table":
			if len(blk.Header) > 0 {
				b.WriteString("| " + strings.Join(mapStrings(blk.Header, mdCell), " | ") + " |\n")
				b.WriteString("|" + strings.Repeat(" --- |", len(blk.Header)) + "\n")
			}
			for _, row := range blk.Rows {
				b.WriteString("| " + strings.Join(mapStrings(row, mdCell), " | ") + " |\n")
			}
		default:
			for _, it := range blk.Items {
				switch blk.Kind {
				case "checklist":
					mark := " "
					if it.Checked {
						mark = "x"
					}
					fmt.Fprintf(&b, "- [%s] %s\n", mark, mdText(it.Text))
				case "risks":
					if it.Level != "" {
						fmt.Fprintf(&b, "- [%s] %s\n", riskLabel[it.Level], mdText(it.Text))
					} else {
						fmt.Fprintf(&b, "- %s\n", mdText(it.Text))
					}
				default:
					fmt.Fprintf(&b, "- %s\n", mdText(it.Text))
				}
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func mapStrings(in []string, f func(string) string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = f(s)
	}
	return out
}

// reportProgressMax caps the per-task progress lines of a report.md without a final.
const reportProgressMax = 20

// progressMarkdown lists one line per task (name, status, duration), capped at reportProgressMax.
func progressMarkdown(rows []reportRow) string {
	if len(rows) == 0 {
		return "(기록된 작업 없음)\n"
	}
	var b strings.Builder
	for i, r := range rows {
		if i == reportProgressMax {
			fmt.Fprintf(&b, "- 외 %d건\n", len(rows)-reportProgressMax)
			break
		}
		dur := "-"
		if r.Seconds != nil {
			dur = reportDuration(*r.Seconds)
		}
		fmt.Fprintf(&b, "- %s · %s · %s\n", mdInline(strings.Join(strings.Fields(r.Name), " ")), reportStatusLabel[r.Status], dur)
	}
	return b.String()
}

// renderReportFile is report.md. The body is the concise report (decision D10): header,
// conclusion, KPI line, then the final blocks (or the progress list when there is no final).
// full only appends the run-record appendix after it.
func renderReportFile(d reportData, sec reportSections, full bool) string {
	k := d.KPI
	title := d.Run.Request
	if d.Final.Present && d.Final.Title != "" {
		title = d.Final.Title
	}
	if strings.TrimSpace(title) == "" {
		title = "orch run 보고서"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n- run: %s\n- 상태: %s\n- 일시: %s\n- 총 소요: %s\n",
		mdHeading(title), d.Run.ID, d.Run.Status, reportDateRange(d, sec), reportDuration(k.WallSeconds))
	if sec.Skipped > 0 {
		fmt.Fprintf(&b, "- task.json 을 읽지 못한 항목: %d\n", sec.Skipped)
	}
	b.WriteString("\n## 결론\n\n")
	switch {
	case !d.Final.Present:
		b.WriteString("Head 최종 보고 없음 — 진행 상황만 표시합니다.\n\n")
	case d.Final.Conclusion != "":
		b.WriteString(mdText(d.Final.Conclusion) + "\n\n")
	default:
		b.WriteString("(결론 없음)\n\n")
	}
	fmt.Fprintf(&b, "- 작업 %d건 · 완료 %d / 실패 %d · 소요 %s\n\n", k.Items, k.Done, k.Failed, reportDuration(k.WallSeconds))
	if d.Final.Present {
		b.WriteString(finalBlocksMarkdown(d.Final.Blocks))
	} else {
		b.WriteString("## 진행\n\n" + progressMarkdown(sec.Rows) + "\n")
	}
	if full {
		b.WriteString("# 실행 기록\n\n")
		fmt.Fprintf(&b, "- 모드: %s · 기타 %d · 작업 합계 %s · 재시도 %d · 폴백 %d\n\n", orDash(d.Run.Mode), k.Other, reportDuration(k.WorkSeconds), k.Retries, k.Fallbacks)
		b.WriteString(d.Markdown)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}
