package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runEndFixture(t *testing.T, mode string, adhoc bool) *Run {
	t.Helper()
	t.Setenv("ORCH_HOME", t.TempDir())
	r := reportRunFixture(t, "run-end", "T1", "closed", reportT0, adhoc, 1)
	r.Mode = mode
	if err := saveRun(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunEndReportWritesHTMLAndWarns(t *testing.T) {
	r := runEndFixture(t, "light", false)
	var out, errb bytes.Buffer
	writeRunEndReport(r.ID, &out, &errb)
	path := filepath.Join(r.dir(), "report.html")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("report.html not written:", err)
	}
	if !strings.Contains(out.String(), "report: "+path) {
		t.Errorf("stdout %q", out.String())
	}
	want := "orch: final.md missing — write the Head final report to " + filepath.Join(r.dir(), "final.md") + " before run end\n"
	if errb.String() != want {
		t.Errorf("stderr %q want %q", errb.String(), want)
	}
}

func TestRunEndReportNoWarningWithFinal(t *testing.T) {
	r := runEndFixture(t, "full", false)
	reportPut(t, filepath.Join(r.dir(), "final.md"), "# t\n\n## 결론\n\n끝\n")
	var out, errb bytes.Buffer
	writeRunEndReport(r.ID, &out, &errb)
	if errb.Len() != 0 || !strings.Contains(out.String(), "report: ") {
		t.Errorf("out %q err %q", out.String(), errb.String())
	}
	page, _ := os.ReadFile(filepath.Join(r.dir(), "report.html"))
	if !strings.Contains(string(page), "끝") {
		t.Error("final conclusion not in html")
	}
}

func TestRunEndReportDirectNoWarning(t *testing.T) {
	r := runEndFixture(t, "direct", false)
	var out, errb bytes.Buffer
	writeRunEndReport(r.ID, &out, &errb)
	if errb.Len() != 0 {
		t.Errorf("direct run warned: %q", errb.String())
	}
}

func TestRunEndReportAdhocSkipped(t *testing.T) {
	r := runEndFixture(t, "light", true)
	var out, errb bytes.Buffer
	writeRunEndReport(r.ID, &out, &errb)
	if _, err := os.Stat(filepath.Join(r.dir(), "report.html")); err == nil || out.Len() != 0 || errb.Len() != 0 {
		t.Error("adhoc run must be skipped silently")
	}
}

func TestRunEndReportFailureIsWarning(t *testing.T) {
	r := runEndFixture(t, "direct", false)
	if err := os.Mkdir(filepath.Join(r.dir(), "report.html"), 0700); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	writeRunEndReport(r.ID, &out, &errb)
	if !strings.Contains(errb.String(), "orch: warning: report:") || out.Len() != 0 {
		t.Errorf("out %q err %q", out.String(), errb.String())
	}
}

func TestHeadGuardAllowsFinalMD(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_RUN", "run-1")
	cases := map[string]bool{
		filepath.Join(runDir("run-1"), "final.md"):               true,
		filepath.Join(runDir("run-1"), "tasks", "x", "final.md"): false,
		filepath.Join(runDir("run-1"), "contracts", "final.md"):  true,
		filepath.Join(runDir("run-1"), "results", "final.md"):    false,
		filepath.Join(runDir("run-1"), "final.md.bak"):           false,
	}
	for path, allowed := range cases {
		got := headExecutionReason(hookJSON("Write", map[string]string{"path": path}))
		if (got == "") != allowed {
			t.Errorf("%s: allowed=%v reason=%q", path, allowed, got)
		}
	}
}

func finalLines(n int) string {
	var b strings.Builder
	b.WriteString("# t\n\n## 결론\n\n끝\n")
	for i := 0; b.Len() > 0 && strings.Count(b.String(), "\n") < n; i++ {
		b.WriteString("- 줄\n")
	}
	return b.String()
}

func TestRunEndReportLongWarns(t *testing.T) {
	r := runEndFixture(t, "full", false)
	reportPut(t, filepath.Join(r.dir(), "final.md"), finalLines(81))
	var out, errb bytes.Buffer
	writeRunEndReport(r.ID, &out, &errb)
	want := "orch: final.md is 81 lines — keep the Head final report to one page (two at most)\n"
	if errb.String() != want {
		t.Errorf("stderr %q want %q", errb.String(), want)
	}
	if _, err := os.Stat(filepath.Join(r.dir(), "report.html")); err != nil {
		t.Error("report.html must still be written:", err)
	}
}

func TestRunEndReportLongBoundaryNoWarning(t *testing.T) {
	r := runEndFixture(t, "full", false)
	reportPut(t, filepath.Join(r.dir(), "final.md"), finalLines(80))
	var out, errb bytes.Buffer
	writeRunEndReport(r.ID, &out, &errb)
	if errb.Len() != 0 {
		t.Errorf("80 lines warned: %q", errb.String())
	}
}
