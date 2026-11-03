package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Captured from vm_stat on this Mac, 2026-11-03.
const hudVMStatSample = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free: 5453.
Pages active: 271527.
Pages inactive: 259920.
Pages speculative: 10496.
Pages wired down: 202882.
Pages purgeable: 550.
File-backed pages: 214718.
Pages stored in compressor: 1063539.
Pages occupied by compressor: 241852.
`

func TestHUDVMStat(t *testing.T) {
	used, err := parseVMStat(hudVMStatSample)
	if err != nil || used != 771409*16384 {
		t.Fatalf("used=%d err=%v", used, err)
	}
	if _, err := parseVMStat(""); err == nil {
		t.Fatal("accepted missing header")
	}
	if _, err := parseVMStat("Mach Virtual Memory Statistics: (page size of 4096 bytes)"); err == nil {
		t.Fatal("accepted missing counters")
	}
	raw := strings.ReplaceAll(hudVMStatSample, "214718", "99999999")
	if used, err := parseVMStat(raw); err != nil || used != 0 {
		t.Fatalf("negative used=%d err=%v", used, err)
	}
	raw = strings.ReplaceAll(hudVMStatSample, "16384", "4096")
	if used, err := parseVMStat(raw); err != nil || used != 771409*4096 {
		t.Fatalf("page size used=%d err=%v", used, err)
	}
}

func TestHUDCPUPercent(t *testing.T) {
	prev := cpuSample{At: time.Now(), User: 10, System: 20, Nice: 5, Idle: 65}
	cur := cpuSample{At: time.Now(), User: 30, System: 30, Nice: 10, Idle: 130}
	for _, tc := range []struct {
		name      string
		prev, cur cpuSample
		want      float64
	}{
		{"normal", prev, cur, 35}, {"equal", prev, prev, 0}, {"missing previous", cpuSample{}, cur, 0}, {"reset", cur, prev, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cpuPercent(tc.prev, tc.cur); math.Abs(got-tc.want) > 0.001 {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestHUDCPUSampleCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "hud-cpu.json")
	sample := cpuSample{At: time.Now().UTC(), User: 12, Idle: 34}
	if err := hudWriteCPUSample(path, sample); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("cache permissions: %v %v", info, err)
	}
	raw, err := os.ReadFile(path)
	var got cpuSample
	if err != nil || json.Unmarshal(raw, &got) != nil || got != sample {
		t.Fatalf("cache roundtrip failed")
	}
}

func TestHUDGeminiLabel(t *testing.T) {
	fake := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(`{"id_token_email":"fake.person@example.test","access_token":"fake-secret"}`))
	if got := hudParseGeminiLabel(fake + "\n"); got != "fake.person" {
		t.Fatalf("label=%q", got)
	}
	for _, raw := range []string{"", "go-keyring-base64:!", "go-keyring-base64:e30=", "plain text"} {
		if hudParseGeminiLabel(raw) != "" {
			t.Fatal("accepted invalid label")
		}
	}
}

// Schema from the live app-server fixture, with synthetic account/credit IDs.
const hudCodexFixture = `{"accountId":"fake-account","ordinaryUsageAllowed":true,"rateLimits":{"primary":{"usedPercent":9,"windowDurationMins":10080,"resetsAt":1793774226},"secondary":null,"planType":"self_serve_business_prolite"},"rateLimitResetCredits":{"availableCount":2,"credits":[{"id":"fake-2","status":"available","expiresAt":1796164240,"title":"Full reset"},{"id":"fake-used","status":"used","expiresAt":1793476561,"title":"Used"},{"id":"fake-1","status":"available","expiresAt":1796068561,"title":"Full reset"}]}}`

func TestHUDCodexMapping(t *testing.T) {
	a, err := hudParseCodex([]byte(hudCodexFixture))
	if err != nil {
		t.Fatal(err)
	}
	if a.FiveHour != nil || a.Weekly == nil || a.Weekly.Used != 9 || a.Weekly.ResetsAt != hudUnixDate(1793774226) || a.Plan != "self_serve_business_prolite" {
		t.Fatalf("bad mapping: %+v", a)
	}
	if a.Coupons == nil || a.Coupons.Available != 2 || len(a.Coupons.Items) != 2 || a.Coupons.NextExpiresAt != hudUnixDate(1796068561) {
		t.Fatalf("bad coupons: %+v", a.Coupons)
	}
	a, err = hudParseCodex([]byte(`{"rateLimits":{"primary":{"usedPercent":12,"windowDurationMins":300},"secondary":{"usedPercent":33,"windowDurationMins":10080}}}`))
	if err != nil || a.FiveHour.Used != 12 || a.Weekly.Used != 33 {
		t.Fatalf("two windows: %+v %v", a, err)
	}
	a, err = hudParseCodex([]byte(`{"rateLimits":{"primary":{"usedPercent":1},"secondary":{"usedPercent":2}}}`))
	if err != nil || a.FiveHour.Used != 1 || a.Weekly.Used != 2 {
		t.Fatalf("positional fallback: %+v %v", a, err)
	}
	for _, raw := range []string{`{}`, `bad`, `{"ordinaryUsageAllowed":false}`} {
		if _, err := hudParseCodex([]byte(raw)); err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}
func TestHUDClaudeMapping(t *testing.T) {
	for _, raw := range []string{
		`{"five_hour":{"utilization":12,"resets_at":"2026-11-03T14:00:00Z"},"seven_day":{"utilization":34,"resets_at":"2026-11-09T14:00:00Z"}}`,
		`{"limits":[{"kind":"session","percent":12,"resets_at":"2026-11-03T14:00:00Z"},{"kind":"weekly","utilization":34,"resets_at":"2026-11-09T14:00:00Z"}]}`,
		`{"five_hour":null,"limits":[{"kind":"session","utilization":12,"resets_at":"2026-11-03T14:00:00Z"},{"kind":"weekly_all","percent":34,"resets_at":"2026-11-09T14:00:00Z"}]}`,
	} {
		a, err := hudParseClaude([]byte(raw))
		if err != nil || a.FiveHour == nil || a.Weekly == nil || a.FiveHour.Used != 12 || a.Weekly.Used != 34 || a.FiveHour.ResetsAt != "2026-11-03T14:00:00Z" {
			t.Fatalf("bad mapping: %+v %v", a, err)
		}
	}
	if _, err := hudParseClaude([]byte(`{"seven_day":null}`)); err == nil {
		t.Fatal("missing windows accepted")
	}
	raw := []byte(`{"claudeAiOauth":{"accessToken":"fixture-secret","expiresAt":1000}}`)
	if _, err := hudParseClaudeCredentials(raw, time.Unix(2, 0)); err == nil || err.Error() != "token expired; open claude to refresh" {
		t.Fatalf("expired credential: %v", err)
	}
	if token, err := hudParseClaudeCredentials(raw, time.Unix(0, 0)); err != nil || token != "fixture-secret" {
		t.Fatal("valid credential rejected")
	}
}
func TestHUDProcessDiscovery(t *testing.T) {
	raw := "100 1 /bin/zsh\n101 100 /bin/cr\n102 101 /bin/codex\n103 102 /bin/claude\n104 100 /bin/sleep\n200 1 node\n201 200 node\nmalformed\n"
	procs := hudParseProcesses(raw)
	if len(procs) != 7 {
		t.Fatalf("parsed %d processes", len(procs))
	}
	if pid, provider := hudFindAgent(100, procs, nil); pid != 102 || provider != "codex" {
		t.Fatalf("got %d %q", pid, provider)
	}
	if pid, provider := hudFindAgent(200, procs, func(pid int) string {
		if pid == 201 {
			return "/usr/bin/node /tools/claude/cli.js"
		}
		return "node unrelated.js"
	}); pid != 201 || provider != "claude" {
		t.Fatalf("node got %d %q", pid, provider)
	}
	if pid, _ := hudFindAgent(104, procs, nil); pid != 0 {
		t.Fatal("unrelated process matched")
	}
	env := "/path/codex --model fake OTHER_CODEX_HOME=wrong CODEX_HOME=/accounts/fake CLAUDE_CONFIG_DIR=/claude/fake PATH=/bin"
	for key, want := range map[string]string{"CODEX_HOME": "/accounts/fake", "CLAUDE_CONFIG_DIR": "/claude/fake", "MISSING": ""} {
		if got := hudEnvValue(env, key); got != want {
			t.Errorf("%s: got %q want %q", key, got, want)
		}
	}
}
func TestHUDRoleLabels(t *testing.T) {
	for _, tc := range []struct{ role, purpose, want string }{{"head", "", "Head"}, {roleRecon, "", "Recon"}, {roleRecon, "web", "Web"}, {roleDesign, "", "Design"}, {roleProduce, "impl", "Impl"}, {roleProduce, "docs", "Docs"}, {roleProduce, "image", "Image"}, {roleIntegrate, "", "Integrate"}, {roleMech, "", "Test"}, {roleCritical, "", "Review"}, {roleNeutral, "", "QA"}, {roleAdversarial, "", "Adversarial"}, {"unknown", "", "unknown"}} {
		if got := hudRoleLabel(tc.role, tc.purpose); got != tc.want {
			t.Errorf("%s/%s: %q", tc.role, tc.purpose, got)
		}
	}
}
func TestHUDArgv(t *testing.T) {
	for _, tc := range []struct {
		argv          []string
		model, effort string
	}{
		{[]string{"codex", "--model", "gpt-fake", "-c", `model_reasoning_effort="high"`}, "gpt-fake", "high"},
		{[]string{"claude", "-m", "sonnet", "--effort", "medium"}, "sonnet", "medium"},
		{[]string{"codex", "--model=gpt-fake", "--config", "model_reasoning_effort='low'"}, "gpt-fake", "low"},
		{[]string{"claude", "--model=opus", "--effort=high"}, "opus", "high"},
		{[]string{"codex", "--model"}, "", ""},
		{[]string{"zsh", "-l"}, "", ""},
	} {
		model, effort := hudArgvModel(tc.argv)
		if model != tc.model || effort != tc.effort {
			t.Errorf("%q: %q %q", tc.argv, model, effort)
		}
	}
	terminals, err := hudParseTerminals([]byte(`{"terminals":[{"address":"t1","program":"codex","argv":["codex","--model","gpt-fake","-c","model_reasoning_effort=high"]},{"address":"t2","agent":"claude-code"}]}`))
	if err != nil || len(terminals) != 2 || terminals[0].Model != "gpt-fake" || terminals[1].Program != "claude-code" {
		t.Fatalf("terminals %+v %v", terminals, err)
	}
}
func TestHUDCache(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	a := HUDAccount{Provider: "codex", Home: "/fake/codex", ID: "codex", Label: "fake", Terminals: []string{"t1"}}
	var calls atomic.Int32
	fetchers := map[string]hudFetchFunc{"codex": func(context.Context, string) (HUDAccount, error) {
		calls.Add(1)
		return HUDAccount{Plan: "pro", Weekly: &HUDWindow{Used: 9}}, nil
	}}
	got := hudCachedUsage(context.Background(), []HUDAccount{a}, 120*time.Second, false, fetchers)
	if calls.Load() != 1 || got[0].Weekly.Used != 9 || got[0].Stale || got[0].CheckedAt.IsZero() {
		t.Fatalf("cold: %+v", got)
	}
	a.Terminals = []string{"t2"}
	got = hudCachedUsage(context.Background(), []HUDAccount{a}, 120*time.Second, false, fetchers)
	if calls.Load() != 1 || got[0].Terminals[0] != "t2" {
		t.Fatal("fresh cache did not preserve current discovery")
	}
	st, err := os.Stat(hudCachePath())
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("cache permission: %v %v", st, err)
	}
	cache := hudReadCache()
	entry := cache[hudAccountKey(a)]
	if len(entry.Account.Terminals) != 0 {
		t.Fatal("cached terminal association")
	}
	entry.FetchedAt = time.Now().Add(-time.Hour)
	cache[hudAccountKey(a)] = entry
	if err = hudWriteCache(cache); err != nil {
		t.Fatal(err)
	}
	fetchers["codex"] = func(context.Context, string) (HUDAccount, error) {
		calls.Add(1)
		return HUDAccount{}, errors.New("offline")
	}
	got = hudCachedUsage(context.Background(), []HUDAccount{a}, 120*time.Second, false, fetchers)
	if !got[0].Stale || got[0].Error != "offline" || got[0].Weekly.Used != 9 || calls.Load() != 2 {
		t.Fatalf("stale fallback: %+v", got)
	}
	missing := a
	missing.Home = "/uncached"
	got = hudCachedUsage(context.Background(), []HUDAccount{missing}, 120*time.Second, false, fetchers)
	if !got[0].Stale || got[0].Error != "offline" || got[0].Weekly != nil {
		t.Fatalf("uncached failure: %+v", got)
	}
}
func TestHUDClaudeCacheAndBackoff(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	a := HUDAccount{Provider: "claude", Home: "", Label: "default", Terminals: []string{}}
	key := hudAccountKey(a)
	entry := hudCacheEntry{Account: HUDAccount{FiveHour: &HUDWindow{Used: 17}}, FetchedAt: time.Now().Add(-100 * time.Second)}
	if err := hudWriteCache(map[string]hudCacheEntry{key: entry}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fetchers := map[string]hudFetchFunc{"claude": func(context.Context, string) (HUDAccount, error) {
		calls++
		return HUDAccount{}, &hudRateLimitError{RetryAt: time.Now().Add(5 * time.Minute)}
	}}
	got := hudCachedUsage(context.Background(), []HUDAccount{a}, 120*time.Second, false, fetchers)
	if calls != 0 || got[0].Stale {
		t.Fatal("Claude minimum freshness not respected")
	}
	entry.FetchedAt = time.Now().Add(-200 * time.Second)
	if err := hudWriteCache(map[string]hudCacheEntry{key: entry}); err != nil {
		t.Fatal(err)
	}
	got = hudCachedUsage(context.Background(), []HUDAccount{a}, 120*time.Second, true, fetchers)
	if calls != 1 || !got[0].Stale || got[0].Error != "HTTP 429" || got[0].FiveHour.Used != 17 {
		t.Fatalf("429 fallback: %+v", got)
	}
	got = hudCachedUsage(context.Background(), []HUDAccount{a}, 0, true, fetchers)
	if calls != 1 || !got[0].Stale || got[0].Error != "HTTP 429" {
		t.Fatal("refresh bypassed Retry-After")
	}
	now := time.Now()
	if got := hudRetryAt("90", now); got.Sub(now) != 90*time.Second {
		t.Fatal("Retry-After seconds")
	}
	if got := hudRetryAt("invalid", now); got.Sub(now) != 5*time.Minute {
		t.Fatal("Retry-After fallback")
	}
	entry = hudReadCache()[key]
	entry.RetryAt = now.Add(-time.Second)
	entry.FetchedAt = now.Add(-time.Hour)
	if err := hudWriteCache(map[string]hudCacheEntry{key: entry}); err != nil {
		t.Fatal(err)
	}
	fetchers["claude"] = func(context.Context, string) (HUDAccount, error) {
		calls++
		return HUDAccount{Weekly: &HUDWindow{Used: 5}}, nil
	}
	got = hudCachedUsage(context.Background(), []HUDAccount{a}, 0, false, fetchers)
	if calls != 2 || got[0].Stale || got[0].Error != "" || !hudReadCache()[key].RetryAt.IsZero() {
		t.Fatal("recovery did not clear backoff")
	}
}
func TestHUDDiscoveryRegistry(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CR_HOME", root)
	raw := `{"ignored":["codex:ignored@example.invalid"],"accounts":[{"provider":"codex","home":"/fake/one","email":"fake@example.invalid","plan":"pro"},{"provider":"codex","home":"/fake/ignored","email":"ignored@example.invalid"},{"provider":"claude","home":"","email":"other@example.invalid"},{"provider":"codex","home":"/fake/one","email":"duplicate@example.invalid"}]}`
	if err := os.WriteFile(filepath.Join(root, "registry.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	accounts, err := hudDiscover(context.Background(), "", false)
	if err != nil || len(accounts) != 2 || accounts[0].Label != "fake" || accounts[1].Home != "" || accounts[1].ID != "default" {
		t.Fatalf("discovery: %+v %v", accounts, err)
	}
	accounts, err = hudDiscover(context.Background(), "", true)
	if err != nil || len(accounts) != 0 {
		t.Fatal("explicit empty terminals must return no accounts")
	}
}
func TestHUDPanesOutput(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("CODEX_LOW_H", "")
	t.Setenv("CODEX_MEDIUM_H", "")
	// A fake hc makes this test independent of the user's running terminals.
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "ls" ]; then
 printf '%s\n' '{"terminals":[{"address":"t-head","agent":"claude"},{"address":"t-codex","agent":"codex"},{"address":"t-shell","agent":"zsh"}]}'
elif [ "$2" = "t-head" ]; then
 printf '%s\n' '{"argv":["env","ORCH_ROLE=worker","/bin/sh","-c","cd /tmp && cr claude -c --model '''sonnet''' --effort '''medium'''"]}'
else
 printf '%s\n' '{"argv":["env","/bin/sh","-c","cd /tmp && cr codex --model gpt-fake -c model_reasoning_effort=\"low\""]}'
fi
`

	if err := os.WriteFile(filepath.Join(bin, "hc"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for _, r := range []*Run{{ID: "active", Status: "active", HeadPane: "t-head", Workspace: "space", Tab: "tab", Mode: "full", Workers: []*Worker{{Name: "worker", Terminal: "t-worker", Pane: "pane", Key: "codex-low-h", Family: "openai", Role: roleProduce, Status: "closed"}}}, {ID: "closed", Status: "closed"}} {
		if err := os.MkdirAll(runDir(r.ID), 0700); err != nil {
			t.Fatal(err)
		}
		if err := saveRun(r); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := cmdHud([]string{"panes"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, &stderr)
	}
	var out HUDPanes
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Runs) != 1 || out.Runs[0].HeadTerminal != "t-head" || len(out.Runs[0].Workers) != 1 {
		t.Fatalf("runs: %+v", out)
	}
	w := out.Runs[0].Workers[0]
	if w.Status != "closed" || w.Label != "Impl" || w.Model != "gpt-6-luna" || w.Effort != "high" {
		t.Fatalf("worker: %+v", w)
	}
	if len(out.Terminals) != 2 || out.Terminals[0].Model != "sonnet" || out.Terminals[0].Effort != "medium" || out.Terminals[1].Model != "gpt-fake" || out.Terminals[1].Effort != "low" {
		t.Fatalf("terminals: %+v", out.Terminals)
	}
}
func TestHUDCommandUsage(t *testing.T) {
	old := hudFindGemini
	hudFindGemini = func() (string, error) { return "", errors.New("not installed") }
	t.Cleanup(func() { hudFindGemini = old })
	for _, args := range [][]string{nil, {"unknown"}, {"sys", "extra"}, {"panes", "extra"}, {"usage", "--max-age", "bad"}, {"usage", "--max-age", "-1s"}, {"usage", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := cmdHud(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") {
			t.Errorf("%q: code %d stdout=%q stderr=%q", args, code, &stdout, &stderr)
		}
	}
	t.Setenv("ORCH_HOME", t.TempDir())
	var out, errout bytes.Buffer
	if code := cmdHud([]string{"usage", "--terminals="}, &out, &errout); code != 0 {
		t.Fatalf("explicit empty: %d %s", code, &errout)
	}
	if !strings.Contains(out.String(), `"accounts":[]`) {
		t.Fatal(fmt.Sprintf("empty list: %s", &out))
	}
}

func TestHUDCodexRPC(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "requests")
	t.Setenv("PATH", bin)
	t.Setenv("HUD_TEST_LOG", log)
	t.Setenv("OPENAI_API_KEY", "fixture-key")
	t.Setenv("CODEX_ACCESS_TOKEN", "fixture-token")
	t.Setenv("HERDR_AGENT", "claude")
	script := `#!/bin/sh
printf '%s\n' "$CODEX_HOME" "${OPENAI_API_KEY-unset}" "${CODEX_ACCESS_TOKEN-unset}" "${HERDR_AGENT-unset}" > "$HUD_TEST_LOG"
IFS= read -r init
IFS= read -r ready
IFS= read -r request
printf '%s\n' "$init" "$ready" "$request" >> "$HUD_TEST_LOG"
printf '%s\n' '{"id":1,"result":{}}' '{"id":2,"result":{"rateLimits":{"planType":"pro","primary":{"usedPercent":23,"windowDurationMins":300,"resetsAt":1793774226}}}}'
while IFS= read -r ignored; do :; done
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := fetchCodexUsage(context.Background(), "/fake/home")
	if err != nil || a.FiveHour == nil || a.FiveHour.Used != 23 {
		t.Fatalf("RPC mapping %+v %v", a, err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 7 || strings.Join(lines[:4], "|") != "/fake/home|unset|unset|unset" {
		t.Fatal("provider environment was not isolated")
	}
	for i, method := range []string{"initialize", "initialized", "account/rateLimits/read"} {
		var call struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal([]byte(lines[4+i]), &call); err != nil || call.Method != method {
			t.Fatalf("bad RPC call %d", i)
		}
		if i == 2 && call.Params["excludeResetCreditDetails"] != false {
			t.Fatal("coupon details excluded")
		}
	}
}

type hudTestTransport func(*http.Request) (*http.Response, error)

func (f hudTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHUDClaudeHTTP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // No security executable: exercise file fallback.
	if err := os.WriteFile(filepath.Join(home, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"fixture-token"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	status := 200
	http.DefaultClient = &http.Client{Transport: hudTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.anthropic.com/api/oauth/usage" || req.Header.Get("Authorization") != "Bearer fixture-token" || req.Header.Get("anthropic-beta") != "oauth-2025-04-20" || req.Header.Get("anthropic-version") != "2023-06-01" {
			t.Error("incorrect Claude usage request")
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"90"}}, Body: io.NopCloser(strings.NewReader(`{"seven_day":{"utilization":11,"resets_at":"2026-11-09T14:00:00Z"}}`))}, nil
	})}
	a, err := fetchClaudeUsage(context.Background(), home)
	if err != nil || a.Weekly == nil || a.Weekly.Used != 11 {
		t.Fatalf("HTTP mapping %+v %v", a, err)
	}
	status = 429
	before := time.Now()
	_, err = fetchClaudeUsage(context.Background(), home)
	var rate *hudRateLimitError
	if !errors.As(err, &rate) || rate.RetryAt.Before(before.Add(90*time.Second)) || rate.RetryAt.After(time.Now().Add(90*time.Second)) {
		t.Fatal("HTTP Retry-After not propagated")
	}
	status = 401
	_, err = fetchClaudeUsage(context.Background(), home)
	if err == nil || err.Error() != "login required" {
		t.Fatal("unauthorized response accepted")
	}
}

func TestHUDCacheMerge(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("codex|/fake/%d", i)
			if err := hudMergeCache(map[string]hudCacheEntry{key: {Account: HUDAccount{Label: "fake"}, FetchedAt: time.Now()}}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if got := len(hudReadCache()); got != 12 {
		t.Fatalf("concurrent cache updates lost: %d/12", got)
	}
}

const hudGeminiFixture = `{"conversation_id":"","status":"SUCCESS","response":"Gemini Models\tWeekly Limit Remaining\t83%\t2026-11-06T22:46:48Z\nGemini Models\tFive Hour Limit Remaining\t89%\t2026-11-03T05:00:04Z\nClaude and GPT models\tWeekly Limit Remaining\t100%\t2026-11-10T04:21:35Z\nClaude and GPT models\tFive Hour Limit Remaining\t100%\t2026-11-03T09:21:35Z\n","duration_seconds":0,"num_turns":0,"usage":{"input_tokens":0,"output_tokens":0,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":0},"command":{"name":"usage","data":{"description":"Within each group, models share a weekly limit and a 5-hour limit. Quota is consumed proportionally to the cost of the tokens. Thus, limits will last longer with shorter tasks or using more cost-effective models. The 5-hour limit smooths out aggregate demand to fairly distribute global capacity across all users, while your weekly limit is tied directly to your individual tier.","groups":[{"name":"Gemini Models","description":"Models within this group: Gemini Flash, Gemini Pro","buckets":[{"id":"gemini-weekly","name":"Weekly Limit Remaining","description":"You have used some of your weekly limit, it will fully refresh in 3 days, 18 hours.","window":"weekly","remaining_fraction":0.8309758305549622,"reset_time":"2026-11-06T22:46:48Z"},{"id":"gemini-5h","name":"Five Hour Limit Remaining","description":"You have used some of your 5-hour limit, it will fully refresh in 38 minutes.","window":"5h","remaining_fraction":0.8854683041572571,"reset_time":"2026-11-03T05:00:04Z"}]},{"name":"Claude and GPT models","description":"Models within this group: Claude Opus, Claude Sonnet, GPT-OSS","buckets":[{"id":"3p-weekly","name":"Weekly Limit Remaining","window":"weekly","remaining_fraction":1,"reset_time":"2026-11-10T04:21:35Z"},{"id":"3p-5h","name":"Five Hour Limit Remaining","window":"5h","remaining_fraction":1,"reset_time":"2026-11-03T09:21:35Z"}]}]}}}`

func TestHUDGeminiMapping(t *testing.T) {
	a, err := hudParseGemini([]byte(hudGeminiFixture))
	if err != nil || a.FiveHour == nil || a.Weekly == nil || a.FiveHour.Used != 11 || a.Weekly.Used != 17 || a.FiveHour.ResetsAt != "2026-11-03T05:00:04Z" || a.Weekly.ResetsAt != "2026-11-06T22:46:48Z" {
		t.Fatalf("gemini: %+v %v", a, err)
	}
	for _, raw := range []string{"bad", "{}", strings.ReplaceAll(hudGeminiFixture, "Gemini Models", "Other Models")} {
		if _, err := hudParseGemini([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestHUDEdgeDiscovery(t *testing.T) {
	raw := "codex CODEX_HOME=/argv/wrong PATH=/bin CODEX_HOME=/Users/x/My Dir/.codex CLAUDE_CONFIG_DIR=/Users/x/Other Dir/.claude LAST=value"
	if got := hudEnvValue(raw, "CODEX_HOME"); got != "/Users/x/My Dir/.codex" {
		t.Fatal(got)
	}
	if got := hudEnvValue(raw, "CLAUDE_CONFIG_DIR"); got != "/Users/x/Other Dir/.claude" {
		t.Fatal(got)
	}
	if got := hudEnvValue("CODEX_HOME=/last space", "CODEX_HOME"); got != "/last space" {
		t.Fatal(got)
	}
	for _, tc := range []struct{ raw, want string }{
		{"node /Users/x/proj/claude-notes/server.js", ""},
		{"node server.js --prompt claude /codex", ""},
		{"node /tools/bin/codex", "codex"},
		{"node /tools/node_modules/@anthropic-ai/claude-code/cli.js", "claude"},
	} {
		if got := hudNodeProvider(tc.raw); got != tc.want {
			t.Errorf("%q: %q", tc.raw, got)
		}
	}
	procs := hudParseProcesses("100 1 /bin/zsh\n101 100 node\n102 101 claude")
	if pid, provider := hudFindAgent(100, procs, func(int) string { return "node /proj/claude-notes/server.js" }); pid != 102 || provider != "claude" {
		t.Fatalf("%d %s", pid, provider)
	}
	t.Setenv("CR_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	accounts, err := hudDiscover(context.Background(), "remote/t1,remote/t2", true)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("remote: %+v %v", accounts, err)
	}
}
func TestHUDWrappedArgv(t *testing.T) {
	for _, tc := range []struct {
		argv          []string
		model, effort string
	}{
		{[]string{"claude", "-c", "--model", "opus"}, "opus", ""},
		{[]string{"claude", "-c", "--model=opus"}, "opus", ""},
		{[]string{"codex", "-c", `model="gpt-5"`, "--config=model_reasoning_effort=high"}, "gpt-5", "high"},
		{[]string{"env", "/bin/sh", "-c", `cd '/tmp/a b' && cr codex -c model="gpt-5" -c model_reasoning_effort="low"`}, "gpt-5", "low"},
		{[]string{`env /bin/sh -c "cd /tmp && cr claude --model 'sonnet' --effort 'medium'"`}, "sonnet", "medium"},
		{[]string{"env", `cd /tmp && cr claude --model 'sonnet' --effort 'medium'`}, "sonnet", "medium"},
	} {
		model, effort := hudArgvModel(tc.argv)
		if model != tc.model || effort != tc.effort {
			t.Errorf("%q: %q %q", tc.argv, model, effort)
		}
	}
	got := hudShellSplit(`one 'two three' "four five" six\ seven`)
	if strings.Join(got, "|") != "one|two three|four five|six seven" {
		t.Fatal(got)
	}
}
func TestHUDFailureBackoffAndFloors(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("ORCH_HOME", t.TempDir())
			a := HUDAccount{Provider: provider, ID: "fixture", Terminals: []string{}}
			key := hudAccountKey(a)
			calls := 0
			fetchers := map[string]hudFetchFunc{provider: func(context.Context, string) (HUDAccount, error) {
				calls++
				return HUDAccount{}, errors.New("offline")
			}}
			for _, refresh := range []bool{false, true, false} {
				got := hudCachedUsage(context.Background(), []HUDAccount{a}, 400*time.Second, refresh, fetchers)
				if calls != 1 || !got[0].Stale || got[0].Error != "offline" {
					t.Fatalf("failure: %d %+v", calls, got)
				}
			}
			entry := hudReadCache()[key]
			if entry.FailAt.IsZero() || entry.Error != "offline" {
				t.Fatalf("missing failure: %+v", entry)
			}
			entry.FailAt = time.Now().Add(-201 * time.Second)
			if err := hudWriteCache(map[string]hudCacheEntry{key: entry}); err != nil {
				t.Fatal(err)
			}
			fetchers[provider] = func(context.Context, string) (HUDAccount, error) {
				calls++
				return HUDAccount{Weekly: &HUDWindow{Used: 3}}, nil
			}
			got := hudCachedUsage(context.Background(), []HUDAccount{a}, 400*time.Second, false, fetchers)
			if calls != 2 || got[0].Stale || !hudReadCache()[key].FailAt.IsZero() {
				t.Fatalf("recovery: %d %+v", calls, got)
			}
			// Even a manual refresh cannot bypass the provider floor.
			hudCachedUsage(context.Background(), []HUDAccount{a}, 0, true, fetchers)
			if calls != 2 {
				t.Fatal("bypassed floor")
			}
			entry = hudReadCache()[key]
			entry.FetchedAt = time.Now().Add(-hudUsageFloor(provider) - time.Second)
			if err := hudWriteCache(map[string]hudCacheEntry{key: entry}); err != nil {
				t.Fatal(err)
			}
			hudCachedUsage(context.Background(), []HUDAccount{a}, 0, true, fetchers)
			if calls != 3 {
				t.Fatal("floor never expired")
			}
		})
	}
	now := time.Now()
	for _, value := range []string{"99999999999", now.Add(24 * time.Hour).UTC().Format(http.TimeFormat)} {
		if got := hudRetryAt(value, now); got.Sub(now) != time.Hour {
			t.Fatalf("uncapped retry: %v", got)
		}
	}
}

func TestHUDSysPartialAndBatchNodeArgs(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	for name, script := range map[string]string{
		"sysctl": "#!/bin/sh\nexit 1\n",
		"ps":     "#!/bin/sh\n[ \"$1 $2 $3 $4\" = \"-o pid=,args= -p 201,202\" ] || exit 1\nprintf '%s\n' '201 node /tools/bin/codex' '202 node /tools/@anthropic-ai/claude-code/cli.js'\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := hudSys(context.Background())
	if err == nil || stats.CPU < 0 || stats.CPU > 100 || stats.MemUsed != 0 || stats.MemTotal != 0 {
		t.Fatalf("partial system: %+v %v", stats, err)
	}
	args := hudNodeArgs(context.Background(), []hudProcess{{201, 1, "node"}, {202, 1, "/usr/bin/node"}, {203, 1, "claude"}})
	if len(args) != 2 || hudNodeProvider(args[201]) != "codex" || hudNodeProvider(args[202]) != "claude" {
		t.Fatalf("batch: %+v", args)
	}
}
