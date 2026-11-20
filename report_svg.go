package main

import (
	"fmt"
	"html"
	"html/template"
	"strconv"
	"strings"
)

// Both charts are plain strings built here; every piece of run text goes through esc.
const (
	svgWidth   = 720
	svgPlotX   = 240
	svgPlotW   = 320
	svgStatX   = 574
	svgRowH    = 22
	svgTopPad  = 26
	svgBarH    = 12
	svgNameMax = 30
)

var reportStatusOrder = []string{"done", "failed", "blocked", "partial", "running", "other"}

func esc(s string) string { return html.EscapeString(s) }

const reportNoItems = `<p class="note">기록된 작업 없음</p>`

// barShape draws one status as pattern + outline, so it reads without colour.
func barShape(status string, x, y, w float64) string {
	var style string
	switch status {
	case "done":
		style = `fill="currentColor"`
	case "failed":
		style = `fill="url(#hatch)" stroke="currentColor" stroke-width="1"`
	case "blocked", "partial":
		style = `fill="none" stroke="currentColor" stroke-width="1.5"`
	case "running":
		style = `fill="none" stroke="currentColor" stroke-width="1.5" stroke-dasharray="4 3"`
	default:
		style = `fill="currentColor" fill-opacity="0.25"`
	}
	return fmt.Sprintf(`<rect class="st-%s" x="%.1f" y="%.1f" width="%.1f" height="%d" %s/>`, statusClass(status), x, y, w, svgBarH, style)
}

func svgText(x, y float64, anchor, text string) string {
	return fmt.Sprintf(`<text x="%.1f" y="%.1f" text-anchor="%s" fill="currentColor">%s</text>`, x, y, anchor, esc(text))
}

var tickSteps = []int{60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400, 172800, 604800}

const secondsPerDay = 86400

// tickStep picks the smallest axis interval with at most 8 ticks from 0 to spanSeconds
// (decision D15); spans beyond the table use whole days.
func tickStep(spanSeconds int) int {
	for _, s := range tickSteps {
		if spanSeconds < 8*s {
			return s
		}
	}
	return (spanSeconds/8/secondsPerDay + 1) * secondsPerDay
}

func minuteLabel(sec int) string {
	return strconv.FormatFloat(float64(sec)/60, 'f', 1, 64)
}

func reportTimelineTotal(d reportData) int {
	total := d.KPI.WallSeconds
	for _, t := range d.Timeline {
		if t.EndS != nil && *t.EndS > total {
			total = *t.EndS
		}
		if t.StartS != nil && *t.StartS > total {
			total = *t.StartS
		}
	}
	return max(total, 1)
}

func timelineSVG(d reportData) template.HTML {
	if len(d.Timeline) == 0 {
		return reportNoItems
	}
	total := reportTimelineTotal(d)
	scale := float64(svgPlotW) / float64(total)
	legendY := svgTopPad + len(d.Timeline)*svgRowH + 10
	height := legendY + 28
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %d %d" role="img" aria-labelledby="tl-title">`, svgWidth, height)
	b.WriteString(`<title id="tl-title">작업 타임라인</title>`)
	b.WriteString(`<defs><pattern id="hatch" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><line x1="0" y1="0" x2="0" y2="6" stroke="currentColor" stroke-width="2"/></pattern></defs>`)
	step := tickStep(total)
	for t := 0; t <= total; t += step {
		x := float64(svgPlotX) + float64(t)*scale
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%d" stroke="currentColor" stroke-opacity="0.25"/>`, x, svgTopPad-6, x, legendY-6)
		b.WriteString(svgText(x, 12, "middle", minuteLabel(t)))
	}
	b.WriteString(svgText(svgPlotX+svgPlotW+4, 12, "start", "분"))
	for i, t := range d.Timeline {
		y := float64(svgTopPad + i*svgRowH)
		fmt.Fprintf(&b, `<g><title>%s</title>`, esc(fmt.Sprintf("%s · %s · %s", t.Name, t.Kind, orDash(t.Key))))
		b.WriteString(svgText(0, y+14, "start", truncRunes(t.Name, svgNameMax)))
		label := reportStatusLabel[t.Status]
		switch {
		case t.StartS == nil:
			label += " · 시작 불명"
		case t.EndS == nil && t.Status == "running":
			end := float64(total)
			b.WriteString(barShape(t.Status, float64(svgPlotX)+float64(*t.StartS)*scale, y+4, max(end-float64(*t.StartS), 1)*scale))
			label += " · 진행 중"
		case t.EndS == nil:
			b.WriteString(barShape(t.Status, float64(svgPlotX)+float64(*t.StartS)*scale, y+4, 2))
			label += " · 종료 불명"
		default:
			b.WriteString(barShape(t.Status, float64(svgPlotX)+float64(*t.StartS)*scale, y+4, max(float64(*t.EndS-*t.StartS)*scale, 2)))
			label += " · " + reportDuration(*t.EndS-*t.StartS)
		}
		b.WriteString(svgText(svgStatX, y+14, "start", label))
		b.WriteString(`</g>`)
	}
	for i, s := range reportStatusOrder {
		x := float64(i * 118)
		fmt.Fprintf(&b, `<rect x="%.1f" y="%d" width="14" height="%d" %s/>`, x, legendY, svgBarH, legendStyle(s))
		b.WriteString(svgText(x+20, float64(legendY)+10, "start", reportStatusLabel[s]))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// legendStyle reuses the barShape attributes for the legend swatches.
func legendStyle(status string) string {
	shape := barShape(status, 0, 0, 0)
	start := strings.Index(shape, `fill=`)
	class := `class="st-` + statusClass(status) + `" `
	return class + strings.TrimSuffix(shape[start:], "/>")
}

func byKeySVG(d reportData) template.HTML {
	if len(d.ByKey) == 0 {
		return reportNoItems
	}
	maxSec := 1
	for _, k := range d.ByKey {
		maxSec = max(maxSec, k.Seconds)
	}
	height := svgTopPad - 10 + len(d.ByKey)*svgRowH + 6
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %d %d" role="img" aria-labelledby="bk-title">`, svgWidth, height)
	b.WriteString(`<title id="bk-title">키별 소요</title>`)
	for i, k := range d.ByKey {
		y := float64(8 + i*svgRowH)
		key := k.Key
		if key == "" {
			key = "(키 없음)"
		}
		fmt.Fprintf(&b, `<g><title>%s</title>`, esc(key))
		b.WriteString(svgText(0, y+14, "start", truncRunes(key, svgNameMax)))
		w := max(float64(k.Seconds)/float64(maxSec)*svgPlotW, 2)
		fmt.Fprintf(&b, `<rect class="st-running" x="%d" y="%.1f" width="%.1f" height="%d" fill="currentColor"/>`, svgPlotX, y+4, w, svgBarH)
		b.WriteString(svgText(svgStatX, y+14, "start", fmt.Sprintf("%d건 · %s", k.N, reportDuration(k.Seconds))))
		b.WriteString(`</g>`)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
