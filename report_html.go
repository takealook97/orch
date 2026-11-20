package main

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

//go:embed templates/report/*
var reportFS embed.FS

type reportKPICell struct{ Label, Value, Unit string }

type reportPage struct {
	reportData
	TokensCSS, ReportCSS                                   template.CSS
	Date, Mode, Status, Wall                               string
	KPIs, FullKPIs                                         []reportKPICell
	Request, Acceptance, Decisions, Work, Findings, Events template.HTML
	Timeline, ByKey                                        template.HTML
	Title, RunClass, DateRange                             string
	Conclusion                                             []string
	Blocks                                                 []finalBlockView
	KPICols, FullKPICols                                   int
	Full, ShowTimeline                                     bool
}

type finalItemView struct {
	Text, Class, Label string
	Checked            bool
}

type finalBlockView struct {
	FinalBlock
	Views    []finalItemView
	TextHTML template.HTML
	Heading  string // card title; empty ## headings get a placeholder
	Label    string // kind label in the card head (decision D13)
}

const untitledBlock = "(제목 없음)"

// finalBlockLabel builds the small kind label shown next to a card title.
func finalBlockLabel(b FinalBlock) string {
	switch b.Kind {
	case "list":
		return fmt.Sprintf("목록 %d개", len(b.Items))
	case "checklist":
		done := 0
		for _, it := range b.Items {
			if it.Checked {
				done++
			}
		}
		return fmt.Sprintf("확인 %d개 중 %d개 완료", len(b.Items), done)
	case "risks":
		counts := map[string]int{}
		for _, it := range b.Items {
			counts[it.Level]++
		}
		var parts []string
		for _, lv := range []string{"high", "medium", "low"} {
			if counts[lv] > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", riskLabel[lv], counts[lv]))
			}
		}
		if len(parts) == 0 {
			return fmt.Sprintf("위험 %d개", len(b.Items))
		}
		return "위험 " + strings.Join(parts, " · ")
	case "table":
		return fmt.Sprintf("표 %d행", len(b.Rows))
	}
	return ""
}

// kpiColumns picks a column count that divides the tile count evenly where possible.
func kpiColumns(n int) int {
	if n%4 == 0 || n%3 != 0 {
		return 4
	}
	return 3
}

var riskClass = map[string]string{"high": "failed", "medium": "warn", "low": "neutral", "": "neutral"}
var riskLabel = map[string]string{"high": "높음", "medium": "중간", "low": "낮음", "": "등급 없음"}

// statusClass maps a task status onto the D5 colour classes.
func statusClass(status string) string {
	switch status {
	case "done", "closed":
		return "done"
	case "failed":
		return "failed"
	case "running", "active":
		return "running"
	case "blocked", "partial":
		return "warn"
	}
	return "neutral"
}

func finalBlockViews(blocks []FinalBlock) ([]finalBlockView, error) {
	out := make([]finalBlockView, 0, len(blocks))
	for _, b := range blocks {
		v := finalBlockView{FinalBlock: b, Heading: b.Title, Label: finalBlockLabel(b)}
		if strings.TrimSpace(b.Title) == "" {
			v.Heading = untitledBlock
		}
		for _, it := range b.Items {
			iv := finalItemView{Text: it.Text, Checked: it.Checked}
			if b.Kind == "risks" {
				iv.Class, iv.Label = riskClass[it.Level], riskLabel[it.Level]
			}
			v.Views = append(v.Views, iv)
		}
		if b.Kind == "text" {
			h, err := markdownHTML(b.Text)
			if err != nil {
				return nil, fmt.Errorf("render %s: %w", b.Title, err)
			}
			v.TextHTML = h
		}
		out = append(out, v)
	}
	return out, nil
}

// imageRenderer replaces Markdown images with a text marker: the report makes no external requests.
type imageRenderer struct{}

func (imageRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindImage, func(w util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, err := w.WriteString("[이미지 생략]")
			return ast.WalkSkipChildren, err
		}
		return ast.WalkContinue, nil
	})
}

// reportMarkdown renders GFM tables only; raw HTML stays disabled (goldmark default), so run text cannot inject tags.
var reportMarkdown = goldmark.New(
	goldmark.WithExtensions(extension.Table),
	goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(imageRenderer{}, 100))),
)

func markdownHTML(src string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := reportMarkdown.Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil // #nosec: goldmark escapes text and omits raw HTML
}

func reportKPICells(k reportKPI) []reportKPICell {
	return []reportKPICell{
		{"작업 항목", fmt.Sprint(k.Items), "건"},
		{"완료", fmt.Sprint(k.Done), "건"},
		{"실패", fmt.Sprint(k.Failed), "건"},
		{"기타", fmt.Sprint(k.Other), "건"},
		{"총 소요", reportDuration(k.WallSeconds), ""},
		{"작업 소요 합계", reportDuration(k.WorkSeconds), ""},
		{"재시도", fmt.Sprint(k.Retries), "회"},
		{"폴백", fmt.Sprint(k.Fallbacks), "건"},
	}
}

// reportConciseKPIs is the three-tile KPI row of the default report (decision D2).
func reportConciseKPIs(k reportKPI) []reportKPICell {
	return []reportKPICell{
		{"작업 수", fmt.Sprint(k.Items), "건"},
		{"완료 / 실패", fmt.Sprintf("%d / %d", k.Done, k.Failed), ""},
		{"소요", reportDuration(k.WallSeconds), ""},
	}
}

// reportDateRange formats "2006-01-02 15:04 → 15:04" (the end gets a date when it differs).
func reportDateRange(d reportData, sec reportSections) string {
	out := sec.Created.Format("2006-01-02 15:04")
	if d.Run.Ended == nil {
		return out + " →"
	}
	end, err := time.Parse(time.RFC3339, *d.Run.Ended)
	if err != nil {
		return out
	}
	end = end.In(sec.Created.Location())
	layout := "15:04"
	if end.Format("2006-01-02") != sec.Created.Format("2006-01-02") {
		layout = "2006-01-02 15:04"
	}
	return out + " → " + end.Format(layout)
}

// renderReportHTML renders the default (concise) page.
func renderReportHTML(d reportData, sec reportSections) ([]byte, error) {
	return renderReportHTMLOpts(d, sec, false)
}

// renderReportHTMLOpts renders the page; full appends the run-record appendix (decision D2).
func renderReportHTMLOpts(d reportData, sec reportSections, full bool) ([]byte, error) {
	tokens, err := reportFS.ReadFile("templates/report/tokens.css")
	if err != nil {
		return nil, err
	}
	css, err := reportFS.ReadFile("templates/report/report.css")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.ParseFS(reportFS, "templates/report/report.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse report template: %w", err)
	}
	page := reportPage{reportData: d, TokensCSS: template.CSS(tokens), ReportCSS: template.CSS(css),
		Date: sec.Created.Format("2006-01-02"), Mode: orDash(d.Run.Mode), Status: d.Run.Status,
		Wall: reportDuration(d.KPI.WallSeconds), KPIs: reportConciseKPIs(d.KPI), KPICols: 3, DateRange: reportDateRange(d, sec),
		Timeline: timelineSVG(d), ByKey: byKeySVG(d), RunClass: statusClass(d.Run.Status), Title: d.Run.Request,
		Full: full, ShowTimeline: !d.Final.Present}
	if full {
		page.FullKPIs = reportKPICells(d.KPI)
		page.FullKPICols = kpiColumns(len(page.FullKPIs))
	}
	if d.Final.Present {
		page.Title = d.Final.Title
		if d.Final.Conclusion != "" {
			page.Conclusion = strings.Split(d.Final.Conclusion, "\n\n")
		}
	}
	if page.Title == "" {
		page.Title = "orch run 보고서"
	}
	if page.Blocks, err = finalBlockViews(d.Final.Blocks); err != nil {
		return nil, err
	}
	if full {
		bodies := reportSectionBodies(d, sec)
		targets := []*template.HTML{&page.Request, &page.Acceptance, &page.Decisions, &page.Work, &page.Findings, &page.Events}
		for i, body := range bodies {
			if *targets[i], err = markdownHTML(body.Body); err != nil {
				return nil, fmt.Errorf("render %s: %w", body.Title, err)
			}
		}
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, page); err != nil {
		return nil, fmt.Errorf("render report html: %w", err)
	}
	return out.Bytes(), nil
}
