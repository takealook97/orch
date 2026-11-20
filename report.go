package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

var errNoReportRun = errors.New("no run found")

// reportRender renders the HTML page; tests replace it to exercise the panic guard.
var reportRender = renderReportHTMLOpts

// guardReport turns a panic raised while building or rendering a report into an error.
func guardReport(build func() ([]byte, error)) (page []byte, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			page, err = nil, fmt.Errorf("report generation panicked: %v", rec)
		}
	}()
	return build()
}

// reportOpen opens path in the default viewer; tests replace it.
var reportOpen = func(path string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return exec.Command("open", path).Start()
}

// selectReportRun applies decision D3: --run, then the terminal's active run, then the
// terminal's latest closed run, then the latest non-adhoc run that has workers or tasks.
func selectReportRun(runID, terminal string) (*Run, error) {
	if runID != "" && terminal != "" {
		return nil, errors.New("--run and --for-terminal cannot be combined")
	}
	if runID != "" {
		return loadRun(runID)
	}
	runs, err := allRuns() // oldest first
	if err != nil {
		return nil, err
	}
	if terminal != "" {
		for _, status := range []string{"active", "closed"} {
			for i := len(runs) - 1; i >= 0; i-- {
				if r := runs[i]; r.HeadPane == terminal && r.Status == status && !r.Adhoc {
					return r, nil
				}
			}
		}
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if r := runs[i]; !r.Adhoc && hasReportItems(r) {
			return r, nil
		}
	}
	return nil, errNoReportRun
}

func writeReportFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0600)
}

func reportCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	runID := fs.String("run", "", "run id")
	terminal := fs.String("for-terminal", "", "Head terminal whose run to report")
	asJSON := fs.Bool("json", false, "print JSON to stdout")
	asHTML := fs.Bool("html", false, "write report.html")
	open := fs.Bool("open", false, "open report.html (macOS)")
	full := fs.Bool("full", false, "append the run record (requests, decisions, tasks, events) to the report")
	pos, err := parse(fs, args)
	if err != nil {
		return 2
	}
	usageErr := func(msg string) int {
		fmt.Fprintln(stderr, "orch report:", msg)
		return 2
	}
	switch {
	case len(pos) != 0:
		return usageErr("unexpected arguments")
	case *runID != "" && *terminal != "":
		return usageErr("--run and --for-terminal cannot be combined")
	case *asJSON && *asHTML:
		return usageErr("--json and --html cannot be combined")
	case *open && !*asHTML:
		return usageErr("--open requires --html")
	}
	r, err := selectReportRun(*runID, *terminal)
	if err != nil {
		fmt.Fprintln(stderr, "orch report:", err)
		return 1
	}
	var d reportData
	var sec reportSections
	if _, err = guardReport(func() (b []byte, e error) { d, sec, e = buildReport(r, time.Now()); return }); err != nil {
		fmt.Fprintln(stderr, "orch report:", err)
		return 1
	}
	switch {
	case *asJSON:
		if err := json.NewEncoder(stdout).Encode(d); err != nil {
			fmt.Fprintln(stderr, "orch report:", err)
			return 1
		}
		return 0
	case *asHTML:
		page, err := guardReport(func() ([]byte, error) { return reportRender(d, sec, *full) })
		if err == nil {
			path := filepath.Join(r.dir(), "report.html")
			if err = writeReportFile(path, page); err == nil {
				fmt.Fprintln(stdout, path)
				if *open {
					if err = reportOpen(path); err != nil {
						fmt.Fprintln(stderr, "orch report: open:", err)
						return 1
					}
				}
				return 0
			}
		}
		fmt.Fprintln(stderr, "orch report:", err)
		return 1
	}
	path := filepath.Join(r.dir(), "report.md")
	if err := writeReportFile(path, []byte(renderReportFile(d, sec, *full))); err != nil {
		fmt.Fprintln(stderr, "orch report:", err)
		return 1
	}
	fmt.Fprintln(stdout, path)
	return 0
}

// writeRunEndReport is the run-end step of decision D7: warn when a non-direct run has no
// final.md, then write report.html. Failures are warnings only; run end already succeeded.
func writeRunEndReport(runID string, stdout, stderr io.Writer) {
	r, err := loadRun(runID)
	if err != nil {
		fmt.Fprintln(stderr, "orch: warning: report:", err)
		return
	}
	if r.Adhoc {
		return
	}
	finalPath := filepath.Join(r.dir(), finalFileName)
	if r.Mode != "direct" && !fileExists(finalPath) {
		fmt.Fprintf(stderr, "orch: final.md missing — write the Head final report to %s before run end\n", finalPath)
	}
	if n := finalLineCount(finalPath); n > finalMaxLines {
		fmt.Fprintf(stderr, "orch: final.md is %d lines — keep the Head final report to one page (two at most)\n", n)
	}
	page, err := guardReport(func() ([]byte, error) {
		d, sec, err := buildReport(r, time.Now())
		if err != nil {
			return nil, err
		}
		return reportRender(d, sec, false)
	})
	if err == nil {
		path := filepath.Join(r.dir(), "report.html")
		if err = writeReportFile(path, page); err == nil {
			fmt.Fprintln(stdout, "report:", path)
			return
		}
	}
	fmt.Fprintln(stderr, "orch: warning: report:", err)
}

// finalMaxLines is the length above which run end warns (decision D4).
const finalMaxLines = 80

// finalLineCount counts the lines of the file at path; unreadable or empty files count 0.
func finalLineCount(path string) int {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return 0
	}
	b = normalizeNewlines(b)
	n := bytes.Count(b, []byte("\n"))
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}
