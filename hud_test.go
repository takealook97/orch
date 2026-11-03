package main

import (
	"bytes"
	"context"
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

// Captured on the development Mac on 2026-11-03. top emits two samples;
// using the first one would report the average since boot instead of recent CPU.
const hudTopSample = `Processes: 589 total, 6 running, 583 sleeping, 5094 threads
CPU usage: 10.0% user, 10.0% sys, 80.0% idle
PhysMem: 14G used (3121M wired, 6535M compressor), 270M unused.
Processes: 589 total, 6 running, 583 sleeping, 5094 threads
2026/11/03 13:03:48
Load Avg: 5.70, 5.04, 3.93
CPU usage: 21.17% user, 17.25% sys, 61.56% idle
SharedLibs: 302M resident, 65M data, 58M linkedit.
MemRegions: 361 total, 14M resident, 125M private, 2214M shared.
PhysMem: 15G used (3121M wired, 6535M compressor), 270M unused.
VM: 299T vsize, 6144M framework vsize, 82061(99) swapins, 371808(0) swapouts.`

func TestHUDTop(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      float64
		ok        bool
	}{{"real last sample", hudTopSample, 38.44, true}, {"idle", "CPU usage: 0% user, 0% sys, 100% idle", 0, true}, {"busy", "CPU usage: 90% user, 10% sys, 0% idle", 100, true}, {"missing", "", 0, false}, {"invalid", "CPU usage: 0% user, 0% sys, 101% idle", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseTopCPU(tc.raw)
			if ok != tc.ok || ok && math.Abs(got-tc.want) > 0.001 {
				t.Fatalf("got %v,%v; want %v,%v", got, ok, tc.want, tc.ok)
			}
		})
	}
	for _, tc := range []struct {
		raw  string
		want uint64
		ok   bool
	}{{hudTopSample, 15 << 30, true}, {"PhysMem: 1.5G used", 3 << 29, true}, {"PhysMem: 123M used", 123 << 20, true}, {"PhysMem: 2T used", 2 << 40, true}, {"PhysMem: 512K used", 512 << 10, true}, {"PhysMem: 42 used", 42, true}, {"PhysMem: unknown", 0, false}} {
		got, ok := parseTopMem(tc.raw)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("memory %q: got %v,%v want %v,%v", tc.raw, got, ok, tc.want, tc.ok)
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
	for _, tc := range []struct{ role, purpose, want string }{{roleRecon, "", "조사"}, {roleRecon, "web", "웹조사"}, {roleDesign, "", "설계검토"}, {roleProduce, "impl", "구현"}, {roleProduce, "docs", "문서"}, {roleProduce, "image", "이미지"}, {roleIntegrate, "", "통합"}, {roleMech, "", "테스트"}, {roleCritical, "", "리뷰"}, {roleNeutral, "", "QA"}, {roleAdversarial, "", "공격검증"}, {"unknown", "", "unknown"}} {
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
	entry := hudCacheEntry{Account: HUDAccount{FiveHour: &HUDWindow{Used: 17}}, FetchedAt: time.Now().Add(-200 * time.Second)}
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
	script := "#!/bin/sh\nprintf '%s\\n' '{\"terminals\":[{\"address\":\"t-head\",\"program\":\"claude\",\"argv\":[\"claude\",\"--model\",\"opus\"]}]}'\n"
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
	if w.Status != "closed" || w.Label != "구현" || w.Model != "gpt-6-luna" || w.Effort != "high" {
		t.Fatalf("worker: %+v", w)
	}
	if len(out.Terminals) != 1 || out.Terminals[0].Model != "opus" {
		t.Fatalf("terminals: %+v", out.Terminals)
	}
}
func TestHUDCommandUsage(t *testing.T) {
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
