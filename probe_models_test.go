package main

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func stubProbe(t *testing.T, fn func(string, string, []string, time.Duration, string) (execResult, error)) {
	t.Helper()
	old := probeExec
	t.Cleanup(func() { probeExec = old })
	probeExec = fn
}
func TestProbeModelsAllKeys(t *testing.T) {
	probeEnv(t)
	overlayFile(t, "models: {sol: future-sol}\n")
	calls := 0
	stubProbe(t, func(cwd, bin string, args []string, timeout time.Duration, log string) (execResult, error) {
		calls++
		if slices.Equal(args, []string{"--version"}) {
			if timeout != 10*time.Second {
				t.Fatalf("version timeout %s", timeout)
			}
			return execResult{}, os.WriteFile(log, []byte(bin+" test-version"), 0600)
		}
		if timeout != 120*time.Second || args[len(args)-1] != "Reply with exactly: OK" || strings.Contains(strings.Join(args, " "), "bypass") {
			t.Fatalf("unsafe probe args: %s %v timeout %s", bin, args, timeout)
		}
		switch bin {
		case "codex":
			if !slices.Contains(args, "read-only") {
				t.Fatal(args)
			}
		case "claude":
			if !slices.Contains(args, "dontAsk") {
				t.Fatal(args)
			}
		case "agy":
			if !slices.Contains(args, "plan") {
				t.Fatal(args)
			}
		}
		return execResult{}, os.WriteFile(log, []byte(`{"result":"OK"}`), 0600)
	})
	var out, errs bytes.Buffer
	code := run([]string{"doctor", "--probe-models"}, strings.NewReader(""), &out, &errs)
	if code != 0 || calls != 14 || strings.Count(out.String(), "ok") != 7 || !strings.Contains(out.String(), "future-sol") || !strings.Contains(out.String(), "source=overlay") || !strings.Contains(out.String(), "test-version") {
		t.Fatalf("code %d calls %d out %s err %s", code, calls, out.String(), errs.String())
	}
}
func TestProbeModelsSelection(t *testing.T) {
	probeEnv(t)
	t.Setenv("CODEX_LOW_H", "env-luna")
	calls := 0
	stubProbe(t, func(cwd, bin string, args []string, timeout time.Duration, log string) (execResult, error) {
		calls++
		if args[0] == "--version" {
			return execResult{}, os.WriteFile(log, []byte("v1"), 0600)
		}
		return execResult{}, os.WriteFile(log, []byte(`{"result":"OK"}`), 0600)
	})
	var out, errs bytes.Buffer
	code := run([]string{"doctor", "--probe-models", "--keys", "codex-medium-h,codex-low-h"}, strings.NewReader(""), &out, &errs)
	if code != 0 || calls != 2 || !strings.Contains(out.String(), "env-luna source=env") {
		t.Fatalf("code %d calls %d out %s err %s", code, calls, out.String(), errs.String())
	}
}
func TestProbeModelsFailures(t *testing.T) {
	for _, mode := range []string{"exit", "timeout", "version", "wrong-answer", "start"} {
		t.Run(mode, func(t *testing.T) {
			probeEnv(t)
			stubProbe(t, func(cwd, bin string, args []string, timeout time.Duration, log string) (execResult, error) {
				if args[0] == "--version" {
					if mode == "version" {
						return execResult{Code: 1}, errors.New("version failed")
					}
					return execResult{}, os.WriteFile(log, []byte("v1"), 0600)
				}
				if mode == "timeout" {
					return execResult{Timed: true}, nil
				}
				if mode == "start" {
					return execResult{}, errors.New("not found")
				}
				if mode == "wrong-answer" {
					return execResult{}, os.WriteFile(log, []byte(`{"result":"NO"}`), 0600)
				}
				if mode == "exit" {
					return execResult{Code: 1}, os.WriteFile(log, []byte(strings.Repeat("가", 400)), 0600)
				}
				return execResult{}, os.WriteFile(log, []byte(`{"result":"OK"}`), 0600)
			})
			var out, errs bytes.Buffer
			code := run([]string{"doctor", "--probe-models", "--keys", "codex-high-l"}, strings.NewReader(""), &out, &errs)
			if code == 0 || !strings.Contains(out.String(), "fail") {
				t.Fatalf("code %d out %s err %s", code, out.String(), errs.String())
			}
			if len([]rune(out.String())) > 400 {
				t.Fatalf("unbounded error excerpt: %d", len([]rune(out.String())))
			}
		})
	}
}
func TestProbeModelsInvalidSelectionAndOptIn(t *testing.T) {
	probeEnv(t)
	stubProbe(t, func(string, string, []string, time.Duration, string) (execResult, error) {
		t.Fatal("unexpected live call")
		return execResult{}, nil
	})
	for _, args := range [][]string{{"doctor", "--keys", "codex-low-h"}, {"doctor", "--probe-models", "--keys", "codex-low-h,bogus"}, {"doctor", "--probe-models", "--keys", ","}, {"doctor", "--unknown"}} {
		var out, errs bytes.Buffer
		if code := run(args, strings.NewReader(""), &out, &errs); code == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
	t.Setenv("ORCH_MUX", "herdr")
	var out, errs bytes.Buffer
	run([]string{"doctor"}, strings.NewReader(""), &out, &errs)
}

func TestProbeModelsWorkerDenied(t *testing.T) {
	probeEnv(t)
	t.Setenv("ORCH_ROLE", "worker")
	stubProbe(t, func(string, string, []string, time.Duration, string) (execResult, error) {
		t.Fatal("worker must not start a model probe")
		return execResult{}, nil
	})
	var out, errs bytes.Buffer
	if code := run([]string{"doctor", "--probe-models", "--keys", "codex-high-l"}, strings.NewReader(""), &out, &errs); code == 0 || !strings.Contains(errs.String(), "Head only") {
		t.Fatalf("code %d out %s err %s", code, out.String(), errs.String())
	}
}

func probeEnv(t *testing.T) { t.Helper(); cleanModelEnv(t); t.Setenv("ORCH_ROLE", "") }

func TestProbeModelsRecordsInvocationSnapshot(t *testing.T) {
	probeEnv(t)
	t.Setenv("CODEX_HIGH_L", "first-sol")
	launched := ""
	stubProbe(t, func(cwd, bin string, args []string, timeout time.Duration, log string) (execResult, error) {
		if args[0] == "--version" {
			t.Setenv("CODEX_HIGH_L", "changed-sol")
			return execResult{}, os.WriteFile(log, []byte("v1"), 0600)
		}
		for i, arg := range args {
			if arg == "--model" {
				launched = args[i+1]
			}
		}
		return execResult{}, os.WriteFile(log, []byte(`{"result":"OK"}`), 0600)
	})
	var out, errs bytes.Buffer
	code := run([]string{"doctor", "--probe-models", "--keys", "codex-high-l"}, strings.NewReader(""), &out, &errs)
	if code != 0 || !strings.Contains(out.String(), "model="+launched+" ") {
		t.Fatalf("launched %s, report %s, code %d stderr %s", launched, out.String(), code, errs.String())
	}
}

func TestProbeModelsHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"doctor", "--help"}} {
		var out, errs bytes.Buffer
		code := run(args, strings.NewReader(""), &out, &errs)
		text := out.String() + errs.String()
		if code != 0 || !strings.Contains(text, "probe-models") || !strings.Contains(text, "keys") {
			t.Fatalf("%v code %d help %s", args, code, text)
		}
	}
}
