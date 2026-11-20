package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// FinalReport mirrors the `final` field of `orch report --json` (decision D2): a fixed interface.
type FinalReport struct {
	Present    bool         `json:"present"`
	Title      string       `json:"title"`
	Conclusion string       `json:"conclusion"`
	Blocks     []FinalBlock `json:"blocks"`
}

type FinalBlock struct {
	Kind   string      `json:"kind"`
	Title  string      `json:"title"`
	Text   string      `json:"text"`
	Items  []FinalItem `json:"items"`
	Header []string    `json:"header"`
	Rows   [][]string  `json:"rows"`
}

type FinalItem struct {
	Text    string `json:"text"`
	Checked bool   `json:"checked"`
	Level   string `json:"level"`
}

const finalFileName = "final.md"

var (
	finalParser     = goldmark.New(goldmark.WithExtensions(extension.Table, extension.TaskList)).Parser()
	finalEntityRef  = regexp.MustCompile(`\\([!-/:-@\[-` + "`" + `{-~])|&(#[xX][0-9a-fA-F]{1,6}|#[0-9]{1,7}|[A-Za-z][A-Za-z0-9]{1,31});`)
	finalRiskLevel  = regexp.MustCompile(`^\[(높음|중간|낮음)\]\s*`)
	finalRiskLevels = map[string]string{"높음": "high", "중간": "medium", "낮음": "low"}
)

// emptyHeadRegexp matches an empty ATX heading of exactly the given level.
func emptyHeadRegexp(level int) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^ {0,3}#{` + strconv.Itoa(level) + `}(?:[ \t]+#*)?[ \t]*$`)
}

// normalizeNewlines turns CRLF and lone CR into LF so offsets and blank-line trims agree.
func normalizeNewlines(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
}

// resolveInlineRefs resolves backslash escapes and entity or numeric references in one pass,
// so the output of one resolution is never resolved again.
func resolveInlineRefs(seg []byte) []byte {
	return finalEntityRef.ReplaceAllFunc(seg, func(m []byte) []byte {
		if m[0] == '\\' {
			return m[1:]
		}
		if m[1] == '#' {
			return util.ResolveNumericReferences(m)
		}
		return util.ResolveEntityNames(m)
	})
}

func emptyFinal() FinalReport {
	return FinalReport{Blocks: []FinalBlock{}}
}

func newFinalBlock(kind, title string) FinalBlock {
	return FinalBlock{Kind: kind, Title: title, Items: []FinalItem{}, Header: []string{}, Rows: [][]string{}}
}

// readFinal loads <run>/final.md; a missing file means "not present".
func readFinal(dir, fallbackTitle string) (FinalReport, error) {
	b, err := os.ReadFile(filepath.Join(dir, finalFileName))
	if os.IsNotExist(err) {
		return emptyFinal(), nil
	}
	if err != nil {
		return emptyFinal(), err
	}
	return parseFinal(b, fallbackTitle), nil
}

type finalSection struct {
	title string
	nodes []ast.Node
	raw   string
}

// nodeStop returns the highest source offset covered by n's own lines or any descendant's.
func nodeStop(n ast.Node) int {
	stop := 0
	if n.Type() != ast.TypeBlock {
		return stop
	}
	if lines := n.Lines(); lines != nil && lines.Len() > 0 {
		stop = lines.At(lines.Len() - 1).Stop
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if s := nodeStop(c); s > stop {
			stop = s
		}
	}
	return stop
}

// headingLineRange returns the byte range of the source line that holds heading h. An empty
// heading ("##") has no lines, so it is located by scanning forward from searchFrom.
func headingLineRange(src []byte, h *ast.Heading, searchFrom int) (int, int) {
	var pos int
	if lines := h.Lines(); lines != nil && lines.Len() > 0 {
		pos = lines.At(0).Start
	} else {
		if searchFrom > len(src) {
			searchFrom = len(src)
		}
		loc := emptyHeadRegexp(h.Level).FindIndex(src[searchFrom:])
		if loc == nil {
			return len(src), len(src)
		}
		pos = searchFrom + loc[0]
	}
	start := bytes.LastIndexByte(src[:pos], '\n') + 1
	end := len(src)
	if i := bytes.IndexByte(src[pos:], '\n'); i >= 0 {
		end = pos + i + 1
	}
	return start, end
}

// trimBlankLines drops leading and trailing blank lines but keeps the first line's indentation.
func trimBlankLines(s string) string {
	i := 0
	for i < len(s) && strings.IndexByte(" \t\r\n", s[i]) >= 0 {
		i++
	}
	if i == len(s) {
		return ""
	}
	lead := strings.LastIndexByte(s[:i], '\n') + 1
	return strings.TrimRight(s[lead:], " \t\r\n")
}

// parseFinal splits final.md into title, conclusion and blocks following decisions D3 and D12.
func parseFinal(md []byte, fallbackTitle string) FinalReport {
	out := FinalReport{Present: true, Title: fallbackTitle, Blocks: []FinalBlock{}}
	md = normalizeNewlines(md)
	doc := finalParser.Parse(text.NewReader(md))
	var sections []*finalSection
	var cur *finalSection
	titleSet := false
	prevEnd, searchFrom := 0, 0
	closeRaw := func(next int) {
		if cur != nil && next >= prevEnd && next <= len(md) {
			cur.raw = trimBlankLines(string(md[prevEnd:next]))
		}
	}
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		h, isHeading := n.(*ast.Heading)
		switch {
		case isHeading && h.Level == 1 && !titleSet:
			titleSet = true
			start, end := headingLineRange(md, h, searchFrom)
			closeRaw(start)
			cur = nil
			searchFrom = max(searchFrom, end)
			if t := strings.TrimSpace(inlineText(h, md)); t != "" {
				out.Title = t
			}
		case isHeading && h.Level == 2:
			start, end := headingLineRange(md, h, searchFrom)
			closeRaw(start)
			cur = &finalSection{title: strings.TrimSpace(inlineText(h, md))}
			sections = append(sections, cur)
			prevEnd = end
			searchFrom = max(searchFrom, end)
		default:
			if cur != nil {
				cur.nodes = append(cur.nodes, n)
			}
		}
		if s := nodeStop(n); s > searchFrom {
			searchFrom = s
		}
	}
	closeRaw(len(md))
	for _, s := range sections {
		if s.title == "결론" {
			out.Conclusion = conclusionText(s.nodes, md)
			continue
		}
		out.Blocks = append(out.Blocks, buildFinalBlock(s, md))
	}
	return out
}

func conclusionText(nodes []ast.Node, src []byte) string {
	var parts []string
	for _, n := range nodes {
		if t := nodePlain(n, src); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// codeBlockText returns the literal content lines of an indented or fenced code block.
func codeBlockText(n ast.Node, src []byte) (string, bool) {
	switch n.(type) {
	case *ast.FencedCodeBlock, *ast.CodeBlock:
	default:
		return "", false
	}
	var b strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		b.Write(seg.Value(src))
	}
	return strings.TrimRight(b.String(), "\r\n"), true
}

// blockPlain flattens one block node to plain text, keeping code block content.
func blockPlain(n ast.Node, src []byte) string {
	if t, ok := codeBlockText(n, src); ok {
		return t
	}
	return strings.TrimSpace(inlineText(n, src))
}

// nodePlain flattens one block node to plain text; list items become lines.
func nodePlain(n ast.Node, src []byte) string {
	if l, ok := n.(*ast.List); ok {
		var lines []string
		for _, it := range listItems(l, src) {
			lines = append(lines, it.text)
		}
		return strings.Join(lines, "\n")
	}
	return blockPlain(n, src)
}

type finalRawItem struct {
	text     string
	hasCheck bool
	checked  bool
}

// listItems flattens a (possibly nested) list into items; GFM task checkboxes are read from the AST.
func listItems(l *ast.List, src []byte) []finalRawItem {
	var out []finalRawItem
	for li := l.FirstChild(); li != nil; li = li.NextSibling() {
		var parts []string
		var nested []*ast.List
		it := finalRawItem{}
		for c := li.FirstChild(); c != nil; c = c.NextSibling() {
			if sub, ok := c.(*ast.List); ok {
				nested = append(nested, sub)
				continue
			}
			for x := c.FirstChild(); x != nil; x = x.NextSibling() {
				if cb, ok := x.(*extast.TaskCheckBox); ok && !it.hasCheck {
					it.hasCheck, it.checked = true, cb.IsChecked
				}
			}
			if t := blockPlain(c, src); t != "" {
				parts = append(parts, t)
			}
		}
		it.text = strings.Join(parts, " ")
		out = append(out, it)
		for _, sub := range nested {
			out = append(out, listItems(sub, src)...)
		}
	}
	return out
}

// inlineText renders inline markdown under n as plain text: code spans keep their content
// literally, everything else resolves backslash escapes and HTML entities.
func inlineText(n ast.Node, src []byte) string {
	var b strings.Builder
	var walk func(ast.Node, bool)
	walk = func(x ast.Node, inCode bool) {
		switch v := x.(type) {
		case *ast.Text:
			seg := v.Segment.Value(src)
			if !inCode {
				seg = resolveInlineRefs(seg)
			}
			b.Write(seg)
			if v.SoftLineBreak() || v.HardLineBreak() {
				b.WriteByte(' ')
			}
			return
		case *ast.String:
			b.Write(v.Value)
			return
		case *ast.RawHTML:
			return
		case *ast.AutoLink:
			b.Write(v.Label(src))
			return
		case *ast.CodeSpan:
			inCode = true
		}
		for c := x.FirstChild(); c != nil; c = c.NextSibling() {
			walk(c, inCode)
		}
	}
	walk(n, false)
	return b.String()
}

func tableCells(row ast.Node, src []byte) []string {
	cells := []string{}
	for c := row.FirstChild(); c != nil; c = c.NextSibling() {
		cells = append(cells, strings.TrimSpace(inlineText(c, src)))
	}
	return cells
}

// onlyBullets reports whether every list is an unordered list.
func onlyBullets(lists []*ast.List) bool {
	for _, l := range lists {
		if l.IsOrdered() {
			return false
		}
	}
	return true
}

func buildFinalBlock(s *finalSection, src []byte) FinalBlock {
	var lists []*ast.List
	var tables []*extast.Table
	paragraphsOnly := true // every node is a paragraph, optionally ending in exactly one table
	for i, n := range s.nodes {
		switch v := n.(type) {
		case *ast.List:
			lists = append(lists, v)
			paragraphsOnly = false
		case *extast.Table:
			tables = append(tables, v)
			if i != len(s.nodes)-1 {
				paragraphsOnly = false
			}
		case *ast.Paragraph, *ast.TextBlock:
		default:
			paragraphsOnly = false
		}
	}
	var items []finalRawItem
	for _, l := range lists {
		items = append(items, listItems(l, src)...)
	}
	checks := 0
	for _, it := range items {
		if it.hasCheck {
			checks++
		}
	}
	isRisk := s.title == "남은 위험" || s.title == "위험"
	isChecklist := s.title == "확인할 것" || (len(items) > 0 && checks*2 > len(items))
	switch {
	case isRisk:
		b := newFinalBlock("risks", s.title)
		for _, it := range items {
			fi := FinalItem{Text: it.text}
			if m := finalRiskLevel.FindStringSubmatch(it.text); m != nil {
				fi.Level = finalRiskLevels[m[1]]
				fi.Text = strings.TrimSpace(it.text[len(m[0]):])
			}
			b.Items = append(b.Items, fi)
		}
		b.Text = proseBefore(s.nodes, src)
		return b
	case isChecklist:
		b := newFinalBlock("checklist", s.title)
		for _, it := range items {
			b.Items = append(b.Items, FinalItem{Text: it.text, Checked: it.hasCheck && it.checked})
		}
		b.Text = proseBefore(s.nodes, src)
		return b
	case len(tables) == 1 && paragraphsOnly:
		b := newFinalBlock("table", s.title)
		for r := tables[0].FirstChild(); r != nil; r = r.NextSibling() {
			if _, ok := r.(*extast.TableHeader); ok {
				b.Header = tableCells(r, src)
			} else {
				b.Rows = append(b.Rows, tableCells(r, src))
			}
		}
		b.Text = proseBefore(s.nodes, src)
		return b
	case len(lists) > 0 && len(lists) == len(s.nodes) && onlyBullets(lists):
		b := newFinalBlock("list", s.title)
		for _, it := range items {
			b.Items = append(b.Items, FinalItem{Text: it.text})
		}
		return b
	}
	b := newFinalBlock("text", s.title)
	b.Text = s.raw
	return b
}

// proseBefore joins the plain text of the non-list, non-table nodes of a section.
func proseBefore(nodes []ast.Node, src []byte) string {
	var parts []string
	for _, n := range nodes {
		switch n.(type) {
		case *ast.List, *extast.Table:
			continue
		}
		if t := nodePlain(n, src); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}
