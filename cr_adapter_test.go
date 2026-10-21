package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCRAdapterSelection(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for adapter tests:", err)
	}
	type account = map[string]any
	a := func(email string, rank any, five, week any) account {
		v := account{"provider": "codex", "email": email, "state": "ready", "five_hour": map[string]any{"remaining": five}, "weekly": map[string]any{"remaining": week}}
		if rank != nil {
			v["rank"] = rank
		}
		return v
	}
	label := func(provider, email string) string {
		return fmt.Sprintf("%s:%x", provider, sha256.Sum256([]byte(provider+":"+strings.ToLower(strings.TrimSpace(email)))))[:len(provider)+11]
	}
	type selectionCase struct {
		name, priority, capacity, provider string
		accounts                           []account
		ignored, excluded                  []string
		want                               string
	}
	cases := []selectionCase{
		{name: "rank beats headroom", priority: "custom", accounts: []account{a("rich@x", 2, 99, 99), a("first@x", 1, 1, 1)}, want: "first@x"},
		{name: "zero last", priority: "custom", accounts: []account{a("a@x", 0, 100, 100), a("z@x", 9, 1, 1)}, want: "z@x"},
		{name: "missing last", priority: "custom", accounts: []account{a("a@x", nil, 100, 100), a("z@x", 9, 1, 1)}, want: "z@x"},
		{name: "local prefix before domain", priority: "custom", accounts: []account{a("aa@a", 1, 99, 99), a("a@z", 1, 1, 1)}, want: "a@z"},
		{name: "full email tie break", priority: "custom", accounts: []account{a("a@z", 1, 99, 99), a("a@a", 1, 1, 1)}, want: "a@a"},
		{name: "unranked email tie break", priority: "custom", accounts: []account{a("z@a", 0, 99, 99), a("a@z", nil, 1, 1)}, want: "a@z"},
		{name: "excluded retry", priority: "custom", accounts: []account{a("a@x", 1, 10, 10), a("b@x", 2, 10, 10)}, excluded: []string{label("codex", "a@x")}, want: "b@x"},
		{name: "unknown remaining usable", priority: "custom", accounts: []account{a("a@x", 1, nil, nil), a("b@x", 2, 99, 99)}, want: "a@x"},
		{name: "legacy minimum first", priority: "plan-high", accounts: []account{a("a@x", 1, 10, 99), a("b@x", 2, 50, 50)}, want: "b@x"},
		{name: "legacy weekly tie", priority: "plan-low", accounts: []account{a("a@x", 1, 20, 30), a("b@x", 2, 20, 90)}, want: "b@x"},
		{name: "default legacy", accounts: []account{a("a@x", 1, 1, 1), a("b@x", 2, 90, 90)}, want: "b@x"},
		{name: "no accounts", priority: "custom"},
	}
	for _, capacity := range []string{"neutral", "most", "least"} {
		cases = append(cases, selectionCase{name: "custom capacity " + capacity, priority: "custom", capacity: capacity, accounts: []account{a("a@x", 1, 10, 10), a("b@x", 2, 90, 90)}, want: "a@x"})
	}
	for _, provider := range []string{"codex", "claude"} {
		bad := []account{a("ignored@x", 1, 100, 100), a("unready@x", 2, 100, 100), a("five@x", 3, 0, 100), a("week@x", 4, 100, 0), a("missing@x", 5, 100, 100), a("other@x", 6, 100, 100), a("good@x", 7, 10, 10)}
		for _, acc := range bad {
			acc["provider"] = provider
		}
		bad[1]["state"] = "unknown"
		bad[4]["home"] = "missing"
		bad[5]["provider"] = map[string]string{"codex": "claude", "claude": "codex"}[provider]
		cases = append(cases, selectionCase{name: provider + " eligibility", priority: "custom", provider: provider, accounts: bad, ignored: []string{provider + ":ignored@x"}, want: "good@x"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "user")
			crHome := filepath.Join(root, "cr")
			for _, dir := range []string{home, crHome} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			provider := tc.provider
			if provider == "" {
				provider = "codex"
			}
			wantHome := ""
			for i, acc := range tc.accounts {
				dir := filepath.Join(root, fmt.Sprintf("account-%d", i))
				if acc["home"] == "missing" {
					acc["home"] = dir
					continue
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				acc["home"] = dir
				if acc["email"] == tc.want {
					wantHome = dir
				}
			}
			data, err := json.Marshal(map[string]any{"priority": tc.priority, "capacity": tc.capacity, "accounts": tc.accounts, "ignored": tc.ignored})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(crHome, "registry.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(python, "-B", "skill/scripts/orch-account-cr", provider, strings.Join(tc.excluded, ","))
			for _, env := range os.Environ() {
				if !strings.HasPrefix(env, "HOME=") && !strings.HasPrefix(env, "CR_HOME=") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env, "HOME="+home, "CR_HOME="+crHome, "PYTHONDONTWRITEBYTECODE=1")
			output, err := cmd.CombinedOutput()
			if tc.want == "" {
				if err == nil {
					t.Fatalf("expected failure, got %s", output)
				}
				return
			}
			if err != nil {
				t.Fatalf("adapter: %v: %s", err, output)
			}
			values := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
				key, value, ok := strings.Cut(line, "=")
				if !ok {
					t.Fatalf("invalid protocol line %q", line)
				}
				values[key] = value
			}
			homeKey := "CODEX_HOME"
			if provider == "claude" {
				homeKey = "CLAUDE_CONFIG_DIR"
			}
			if values["LABEL"] != label(provider, tc.want) || values[homeKey] != wantHome {
				t.Fatalf("selected %v; want %s at %s", values, tc.want, wantHome)
			}
		})
	}
}
