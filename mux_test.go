package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// Legacy fixtures must not inherit the real terminal running the test suite.
// App tests install explicit stubs; an unstubbed app operation must never reach live Herdr.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("HC_TERMINAL_ID")
	_ = os.Unsetenv("ORCH_MUX")
	herdrRun = func(args ...string) ([]byte, error) { return nil, fmt.Errorf("unstubbed herdr: %v", args) }
	hcRun = func(args ...string) ([]byte, error) { return nil, fmt.Errorf("unstubbed hc: %v", args) }
	appRun = func(args ...string) ([]byte, error) { return nil, fmt.Errorf("unstubbed app: %v", args) }
	hcRPC = func(method string, params any) (json.RawMessage, error) {
		return nil, fmt.Errorf("unstubbed RPC: %s", method)
	}
	os.Exit(m.Run())
}

func TestMuxDetection(t *testing.T) {
	for _, tt := range []struct{ name, override, legacy, terminal, want, id string }{
		{"outside", "", "", "", "", "legacy-head"},
		{"legacy", "", "1", "", "herdr", "legacy-head"},
		{"app", "", "", "t-head", "app", "t-head"},
		{"legacy precedes app", "", "1", "t-head", "herdr", "legacy-head"},
		{"override app", "app", "1", "t-head", "app", "t-head"},
		{"override legacy", "herdr", "", "t-head", "herdr", "legacy-head"},
		{"invalid override", "bad", "1", "t-head", "", "legacy-head"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ORCH_MUX", tt.override)
			t.Setenv("HERDR_ENV", tt.legacy)
			t.Setenv("HC_TERMINAL_ID", tt.terminal)
			t.Setenv("HERDR_PANE_ID", "legacy-head")
			if got := backend(); got != tt.want {
				t.Fatalf("backend=%q want %q", got, tt.want)
			}
			if got := headID(); got != tt.id {
				t.Fatalf("headID=%q want %q", got, tt.id)
			}
		})
	}
}
