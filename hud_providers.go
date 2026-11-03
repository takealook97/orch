package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func hudProviderEnv(home string) []string {
	env := []string{}
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		switch key {
		case "CODEX_HOME", "CLAUDE_CONFIG_DIR", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_ACCESS_TOKEN", "CR_CODEX_NO_DAEMON", "CR_LAUNCH_EMAIL", "HERDR_AGENT":
			continue
		}
		env = append(env, e)
	}
	return append(env, "CODEX_HOME="+home)
}
func hudCodexBinary() (string, error) {
	if path, err := exec.LookPath("codex"); err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	for _, path := range []string{filepath.Join(home, ".local", "bin", "codex"), "/opt/homebrew/bin/codex", "/usr/local/bin/codex"} {
		if st, err := os.Stat(path); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			return path, nil
		}
	}
	return "", errors.New("codex binary not found")
}
func fetchCodexUsage(ctx context.Context, home string) (HUDAccount, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	path, err := hudCodexBinary()
	if err != nil {
		return HUDAccount{}, err
	}
	cmd := exec.CommandContext(ctx, path, "app-server", "-c", "cli_auth_credentials_store=file")
	cmd.Env = hudProviderEnv(home)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return HUDAccount{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return HUDAccount{}, err
	}
	if err = cmd.Start(); err != nil {
		stdin.Close()
		return HUDAccount{}, errors.New("codex app-server failed to start")
	}
	defer func() { stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// A descendant retaining stdout must not keep a HUD invocation alive beyond
	// the deadline after CommandContext has killed the app-server process.
	stopRead := context.AfterFunc(ctx, func() { _ = stdout.Close() })
	defer stopRead()
	encoder := json.NewEncoder(stdin)
	for _, call := range []any{
		map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "orch-hud", "version": "0.1.0"}}},
		map[string]any{"method": "initialized", "params": map[string]any{}},
		map[string]any{"id": 2, "method": "account/rateLimits/read", "params": map[string]any{"excludeResetCreditDetails": false}},
	} {
		if encoder.Encode(call) != nil {
			return HUDAccount{}, errors.New("codex app-server write failed")
		}
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &response) != nil {
			continue
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			return HUDAccount{}, errors.New("codex app-server RPC error")
		}
		if response.ID == 2 {
			return hudParseCodex(response.Result)
		}
	}
	if ctx.Err() != nil {
		return HUDAccount{}, errors.New("codex usage timed out")
	}
	return HUDAccount{}, errors.New("codex app-server stopped without usage")
}
func hudUnixDate(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}
func hudParseCodex(raw []byte) (HUDAccount, error) {
	type window struct {
		Used    float64 `json:"usedPercent"`
		Reset   int64   `json:"resetsAt"`
		Minutes int     `json:"windowDurationMins"`
	}
	var body struct {
		Allowed *bool `json:"ordinaryUsageAllowed"`
		Limits  struct {
			Primary   *window `json:"primary"`
			Secondary *window `json:"secondary"`
			Plan      string  `json:"planType"`
		} `json:"rateLimits"`
		Credits *struct {
			Count int `json:"availableCount"`
			Items []struct {
				Title   string `json:"title"`
				Status  string `json:"status"`
				Expires int64  `json:"expiresAt"`
			} `json:"credits"`
		} `json:"rateLimitResetCredits"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return HUDAccount{}, errors.New("invalid codex usage JSON")
	}
	if body.Allowed != nil && !*body.Allowed {
		return HUDAccount{}, errors.New("ordinary usage unavailable")
	}
	a := HUDAccount{Plan: body.Limits.Plan}
	for i, w := range []*window{body.Limits.Primary, body.Limits.Secondary} {
		if w == nil {
			continue
		}
		value := &HUDWindow{Used: w.Used, ResetsAt: hudUnixDate(w.Reset)}
		switch {
		case w.Minutes == 300:
			a.FiveHour = value
		case w.Minutes == 10080:
			a.Weekly = value
		case i == 0:
			a.FiveHour = value
		default:
			a.Weekly = value
		}
	}
	if body.Credits != nil {
		a.Coupons = &HUDCoupons{Available: body.Credits.Count, Items: []HUDCoupon{}}
		for _, item := range body.Credits.Items {
			if item.Status == "available" {
				a.Coupons.Items = append(a.Coupons.Items, HUDCoupon{item.Title, hudUnixDate(item.Expires)})
			}
		}
		hudSortCoupons(a.Coupons)
	}
	if a.FiveHour == nil && a.Weekly == nil && a.Coupons == nil {
		return a, errors.New("codex usage response has no supported limits")
	}
	return a, nil
}

func hudClaudeToken(ctx context.Context, home string) (string, error) {
	var raw []byte
	if runtime.GOOS == "darwin" {
		service := "Claude Code-credentials"
		if home != "" {
			sum := sha256.Sum256([]byte(home))
			service += "-" + hex.EncodeToString(sum[:])[:8]
		}
		raw, _ = exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-w").Output()
	}
	if len(raw) == 0 {
		path := filepath.Join(home, ".credentials.json")
		if home == "" {
			user, _ := os.UserHomeDir()
			path = filepath.Join(user, ".claude", ".credentials.json")
		}
		raw, _ = os.ReadFile(path)
	}
	return hudParseClaudeCredentials(raw, time.Now())
}
func hudParseClaudeCredentials(raw []byte, now time.Time) (string, error) {
	var body struct {
		OAuth struct {
			Token   string `json:"accessToken"`
			Expires int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(raw, &body) != nil || body.OAuth.Token == "" {
		return "", errors.New("login required")
	}
	if body.OAuth.Expires > 0 && !now.Before(time.UnixMilli(body.OAuth.Expires)) {
		return "", errors.New("token expired; open claude to refresh")
	}
	return body.OAuth.Token, nil
}
func fetchClaudeUsage(ctx context.Context, home string) (HUDAccount, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	token, err := hudClaudeToken(ctx, home)
	if err != nil {
		return HUDAccount{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil)
	if err != nil {
		return HUDAccount{}, errors.New("claude usage request failed")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return HUDAccount{}, errors.New("claude usage request failed")
	}
	defer res.Body.Close()
	if res.StatusCode == 429 {
		return HUDAccount{}, &hudRateLimitError{RetryAt: hudRetryAt(res.Header.Get("Retry-After"), time.Now())}
	}
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return HUDAccount{}, errors.New("login required")
	}
	if res.StatusCode != 200 {
		return HUDAccount{}, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return HUDAccount{}, errors.New("claude usage read failed")
	}
	return hudParseClaude(raw)
}
func hudRetryAt(value string, now time.Time) time.Time {
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		if seconds > 3600 {
			seconds = 3600
		}
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		if at.After(now.Add(time.Hour)) {
			return now.Add(time.Hour)
		}
		return at
	}
	return now.Add(5 * time.Minute)
}
func hudParseClaude(raw []byte) (HUDAccount, error) {
	type window struct {
		Used   *float64 `json:"utilization"`
		Resets string   `json:"resets_at"`
		Reset  string   `json:"reset_at"`
	}
	var body struct {
		Five   *window `json:"five_hour"`
		Seven  *window `json:"seven_day"`
		Limits []struct {
			Kind    string   `json:"kind"`
			Used    *float64 `json:"utilization"`
			Percent *float64 `json:"percent"`
			Resets  string   `json:"resets_at"`
		} `json:"limits"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return HUDAccount{}, errors.New("invalid claude usage JSON")
	}
	date := func(s string) string {
		at, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return ""
		}
		return at.UTC().Format(time.RFC3339)
	}
	parse := func(w *window) *HUDWindow {
		if w == nil || w.Used == nil {
			return nil
		}
		s := w.Resets
		if s == "" {
			s = w.Reset
		}
		return &HUDWindow{Used: *w.Used, ResetsAt: date(s)}
	}
	a := HUDAccount{FiveHour: parse(body.Five), Weekly: parse(body.Seven)}
	for _, limit := range body.Limits {
		v := limit.Percent
		if v == nil {
			v = limit.Used
		}
		if v == nil {
			continue
		}
		w := &HUDWindow{Used: *v, ResetsAt: date(limit.Resets)}
		if limit.Kind == "session" && a.FiveHour == nil {
			a.FiveHour = w
		}
		if (limit.Kind == "weekly" || limit.Kind == "weekly_all") && a.Weekly == nil {
			a.Weekly = w
		}
	}
	if a.FiveHour == nil && a.Weekly == nil {
		return a, errors.New("usage response has no supported windows")
	}
	return a, nil
}

func hudGeminiBinary() (string, error) {
	if path, err := exec.LookPath("agy"); err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	for _, path := range []string{"/opt/homebrew/bin/agy", filepath.Join(home, ".local", "bin", "agy")} {
		if st, err := os.Stat(path); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			return path, nil
		}
	}
	return "", errors.New("agy binary not found")
}
func fetchGeminiUsage(ctx context.Context, _ string) (HUDAccount, error) {
	path, err := hudGeminiBinary()
	if err != nil {
		return HUDAccount{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 22*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-p", "/usage", "--output-format", "json", "--print-timeout", "20s")
	cmd.Dir = os.TempDir()
	raw, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return HUDAccount{}, errors.New("gemini usage timed out")
		}
		return HUDAccount{}, errors.New("gemini usage command failed")
	}
	return hudParseGemini(raw)
}
func hudParseGemini(raw []byte) (HUDAccount, error) {
	var body struct {
		Command struct {
			Data struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []struct {
						Window    string   `json:"window"`
						Remaining *float64 `json:"remaining_fraction"`
						Reset     string   `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return HUDAccount{}, errors.New("invalid gemini usage JSON")
	}
	a := HUDAccount{}
	for _, group := range body.Command.Data.Groups {
		if !strings.HasPrefix(group.Name, "Gemini") {
			continue
		}
		for _, b := range group.Buckets {
			if b.Remaining == nil || *b.Remaining < 0 || *b.Remaining > 1 {
				continue
			}
			at, err := time.Parse(time.RFC3339, b.Reset)
			if err != nil {
				continue
			}
			w := &HUDWindow{Used: math.Round((1 - *b.Remaining) * 100), ResetsAt: at.UTC().Format(time.RFC3339)}
			switch b.Window {
			case "5h":
				a.FiveHour = w
			case "weekly":
				a.Weekly = w
			}
		}
		break
	}
	if a.FiveHour == nil && a.Weekly == nil {
		return a, errors.New("gemini usage response has no supported windows")
	}
	return a, nil
}

// Only the email local part leaves this function; credentials are never cached.
func hudGeminiLabel(ctx context.Context) string {
	raw, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "gemini", "-a", "antigravity", "-w").Output()
	if err != nil {
		return ""
	}
	return hudParseGeminiLabel(string(raw))
}

func hudParseGeminiLabel(raw string) string {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(raw), "go-keyring-base64:")
	if !ok {
		return ""
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	var value struct {
		Email string `json:"id_token_email"`
	}
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	local, domain, ok := strings.Cut(value.Email, "@")
	if !ok || domain == "" {
		return ""
	}
	return local
}
