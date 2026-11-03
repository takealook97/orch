package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type HUDWindow struct {
	Used     float64 `json:"used_pct"`
	ResetsAt string  `json:"resets_at"`
}
type HUDCoupon struct {
	Title     string `json:"title"`
	ExpiresAt string `json:"expires_at"`
}
type HUDCoupons struct {
	Available     int         `json:"available"`
	NextExpiresAt string      `json:"next_expires_at"`
	Items         []HUDCoupon `json:"items"`
}
type HUDAccount struct {
	Selected  bool        `json:"selected,omitempty"`
	Provider  string      `json:"provider"`
	Home      string      `json:"home"`
	ID        string      `json:"id"`
	Label     string      `json:"label"`
	Plan      string      `json:"plan"`
	FiveHour  *HUDWindow  `json:"five_hour"`
	Weekly    *HUDWindow  `json:"weekly"`
	Coupons   *HUDCoupons `json:"coupons"`
	Terminals []string    `json:"terminals"`
	CheckedAt time.Time   `json:"checked_at"`
	Stale     bool        `json:"stale"`
	Error     string      `json:"error"`
}
type HUDUsage struct {
	At       time.Time    `json:"at"`
	Accounts []HUDAccount `json:"accounts"`
	Error    string       `json:"error,omitempty"`
}
type hudRegistryWindow struct {
	Remaining *float64  `json:"remaining,omitempty"`
	ResetsAt  time.Time `json:"resets_at,omitempty"`
}
type hudRegistryAccount struct {
	Provider     string            `json:"provider"`
	Home         string            `json:"home"`
	Email        string            `json:"email"`
	Plan         string            `json:"plan"`
	PlanOverride string            `json:"plan_override"`
	State        string            `json:"state"`
	Rank         int               `json:"rank"`
	FiveHour     hudRegistryWindow `json:"five_hour"`
	Weekly       hudRegistryWindow `json:"weekly"`
}
type hudRegistry struct {
	Accounts    []hudRegistryAccount `json:"accounts"`
	Ignored     []string             `json:"ignored"`
	Priority    string               `json:"priority"`
	Capacity    string               `json:"capacity"`
	PlanWeights map[string]int       `json:"plan_weights"`
}

func hudReadRegistry() (hudRegistry, error) {
	home, _ := os.UserHomeDir()
	root := os.Getenv("CR_HOME")
	if root == "" {
		root = filepath.Join(home, ".local", "share", "cr")
	}
	var r hudRegistry
	raw, err := os.ReadFile(filepath.Join(root, "registry.json"))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}
func hudCanonicalHome(provider, home string) string {
	if provider == "codex" && home == "" {
		user, _ := os.UserHomeDir()
		return filepath.Join(user, ".codex")
	}
	return home
}
func hudAccountKey(a HUDAccount) string { return a.Provider + "|" + a.Home }
func hudAccount(provider, home string, r hudRegistry) HUDAccount {
	home = hudCanonicalHome(provider, home)
	id := filepath.Base(home)
	if home == "" {
		id = "default"
	}
	a := HUDAccount{Provider: provider, Home: home, ID: id, Label: "default", Terminals: []string{}}
	for _, entry := range r.Accounts {
		if entry.Provider == provider && hudCanonicalHome(provider, entry.Home) == home {
			a.Label = strings.SplitN(entry.Email, "@", 2)[0]
			if a.Label == "" {
				a.Label = "default"
			}
			a.Plan = entry.Plan
			break
		}
	}
	return a
}

type hudProcess struct {
	PID, PPID int
	Comm      string
}

func hudParseProcesses(raw string) []hudProcess {
	out := []hudProcess{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 == nil && e2 == nil {
			out = append(out, hudProcess{pid, ppid, strings.Join(f[2:], " ")})
		}
	}
	return out
}

// Breadth-first traversal prefers the agent closest to the terminal's shell.
func hudFindAgent(root int, procs []hudProcess, args func(int) string) (int, string) {
	children := map[int][]hudProcess{}
	byID := map[int]hudProcess{}
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p)
		byID[p.PID] = p
	}
	queue := []int{root}
	seen := map[int]bool{}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		p := byID[pid]
		name := filepath.Base(p.Comm)
		if name == "codex" || name == "claude" {
			return pid, name
		}
		if name == "node" && args != nil {
			if provider := hudNodeProvider(args(pid)); provider != "" {
				return pid, provider
			}
		}
		for _, child := range children[pid] {
			queue = append(queue, child.PID)
		}
	}
	return 0, ""
}
func hudNodeProvider(raw string) string {
	argv := hudShellSplit(raw)
	if len(argv) < 2 {
		return ""
	}
	script := argv[1]
	switch filepath.Base(script) {
	case "codex":
		return "codex"
	case "claude":
		return "claude"
	case "cli.js":
		if strings.Contains(script, "/@anthropic-ai/claude-code/") || strings.HasSuffix(script, "/claude/cli.js") {
			return "claude"
		}
	}
	return ""
}

var hudEnvBoundary = regexp.MustCompile(`(?:^| )[A-Z_][A-Z0-9_]*=`)

func hudEnvValue(raw, key string) string {
	matches := hudEnvBoundary.FindAllStringIndex(strings.TrimSpace(raw), -1)
	raw = strings.TrimSpace(raw)
	value := ""
	for i, m := range matches {
		start := m[0]
		if raw[start] == ' ' {
			start++
		}
		if raw[start:m[1]] != key+"=" {
			continue
		}
		end := len(raw)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		value = raw[m[1]:end]
	}
	return value
}
func hudNodeArgs(ctx context.Context, procs []hudProcess) map[int]string {
	ids := []string{}
	for _, p := range procs {
		if filepath.Base(p.Comm) == "node" {
			ids = append(ids, strconv.Itoa(p.PID))
		}
	}
	out := map[int]string{}
	if len(ids) == 0 {
		return out
	}
	raw, err := exec.CommandContext(ctx, "ps", "-o", "pid=,args=", "-p", strings.Join(ids, ",")).Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err == nil {
			out[pid] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
		}
	}
	return out
}
func hudDiscover(ctx context.Context, terminals string, specified bool) ([]HUDAccount, error) {
	r, err := hudReadRegistry()
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	accounts := []HUDAccount{}
	indices := map[string]int{}
	add := func(a HUDAccount, terminal string) {
		key := hudAccountKey(a)
		i, ok := indices[key]
		if !ok {
			i = len(accounts)
			indices[key] = i
			accounts = append(accounts, a)
		}
		if terminal != "" && !contains1(accounts[i].Terminals, terminal) {
			accounts[i].Terminals = append(accounts[i].Terminals, terminal)
		}
	}
	if !specified {
		for _, entry := range r.Accounts {
			if entry.Provider != "codex" && entry.Provider != "claude" {
				continue
			}
			if contains1(r.Ignored, entry.Provider+":"+strings.ToLower(strings.TrimSpace(entry.Email))) {
				continue
			}
			add(hudAccount(entry.Provider, entry.Home, r), "")
		}
		return accounts, nil
	}
	local := []string{}
	for _, address := range strings.Split(terminals, ",") {
		address = strings.TrimSpace(address)
		if address != "" && !strings.Contains(address, "/") {
			local = append(local, address)
		}
	}
	terminals = strings.Join(local, ",")
	if terminals == "" {
		return accounts, nil
	}
	raw, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,comm=").Output()
	if err != nil {
		return accounts, fmt.Errorf("ps: %w", err)
	}
	procs := hudParseProcesses(string(raw))
	nodeArgs := hudNodeArgs(ctx, procs)
	var failures []string
	for _, address := range strings.Split(terminals, ",") {
		address = strings.TrimSpace(address)
		if address == "" {
			continue
		}
		if strings.Contains(address, "/") {
			continue
		}
		raw, err := exec.CommandContext(ctx, "hc", "get", address, "--json").Output()
		if err != nil {
			failures = append(failures, "terminal lookup failed")
			continue
		}
		var terminal struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(raw, &terminal) != nil || terminal.PID == 0 {
			failures = append(failures, "terminal has no pid")
			continue
		}
		pid, provider := hudFindAgent(terminal.PID, procs, func(pid int) string { return nodeArgs[pid] })
		if pid == 0 {
			continue
		}
		raw, err = exec.CommandContext(ctx, "ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			failures = append(failures, "agent environment unavailable")
			continue
		}
		key := "CODEX_HOME"
		if provider == "claude" {
			key = "CLAUDE_CONFIG_DIR"
		}
		add(hudAccount(provider, hudEnvValue(string(raw), key), r), address)
	}
	if len(failures) > 0 {
		return accounts, errors.New(strings.Join(failures, "; "))
	}
	return accounts, nil
}

type hudCacheEntry struct {
	Account   HUDAccount `json:"account"`
	FetchedAt time.Time  `json:"fetched_at"`
	RetryAt   time.Time  `json:"retry_at,omitempty"`
	FailAt    time.Time  `json:"fail_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}
type hudFetchFunc func(context.Context, string) (HUDAccount, error)
type hudRateLimitError struct{ RetryAt time.Time }

func (e *hudRateLimitError) Error() string { return "HTTP 429" }

var hudFindGemini = hudGeminiBinary

func hudUsage(ctx context.Context, terminals string, specified bool, age time.Duration, refresh bool) HUDUsage {
	return hudUsageMode(ctx, terminals, specified, age, refresh, false)
}
func hudUsageMode(ctx context.Context, terminals string, specified bool, age time.Duration, refresh, selected bool) HUDUsage {
	out := HUDUsage{At: time.Now(), Accounts: []HUDAccount{}}
	var accounts []HUDAccount
	var err error
	if selected {
		accounts, err = hudDiscoverSelected(out.At)
	} else {
		accounts, err = hudDiscover(ctx, terminals, specified)
	}
	if err != nil {
		out.Error = err.Error()
	}
	if _, err := hudFindGemini(); err == nil {
		accounts = append(accounts, HUDAccount{Provider: "gemini", ID: "agy", Label: hudGeminiLabel(ctx), Selected: selected, Terminals: []string{}})
	}
	out.Accounts = hudCachedUsage(ctx, accounts, age, refresh, map[string]hudFetchFunc{"codex": fetchCodexUsage, "claude": fetchClaudeUsage, "gemini": fetchGeminiUsage})
	return out
}
func hudCachePath() string { return filepath.Join(orchHome(), "cache", "hud-usage.json") }
func hudReadCache() map[string]hudCacheEntry {
	cache := map[string]hudCacheEntry{}
	raw, err := os.ReadFile(hudCachePath())
	if err == nil {
		_ = json.Unmarshal(raw, &cache)
	}
	if cache == nil {
		cache = map[string]hudCacheEntry{}
	}
	return cache
}
func hudWriteCache(cache map[string]hudCacheEntry) error {
	path := hudCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".hud-usage-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Defaults and active-space polls may overlap. Merge only fetched entries under
// a short lock so one poll cannot erase another account's value/backoff.
func hudMergeCache(updates map[string]hudCacheEntry) error {
	if len(updates) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(hudCachePath()), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(hudCachePath()+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	cache := hudReadCache()
	for key, entry := range updates {
		cache[key] = entry
	}
	return hudWriteCache(cache)
}

// Discovery metadata is always current; cache entries never retain terminal lists.
func hudOverlay(cached, identity HUDAccount) HUDAccount {
	cached.Selected = identity.Selected
	cached.Provider = identity.Provider
	cached.Home = identity.Home
	cached.ID = identity.ID
	cached.Label = identity.Label
	cached.Terminals = identity.Terminals
	if cached.Plan == "" {
		cached.Plan = identity.Plan
	}
	return cached
}
func hudCachedUsage(ctx context.Context, accounts []HUDAccount, age time.Duration, refresh bool, fetchers map[string]hudFetchFunc) []HUDAccount {
	cache := hudReadCache()
	updates := map[string]hudCacheEntry{}
	out := make([]HUDAccount, len(accounts))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, a := range accounts {
		wg.Add(1)
		go func(i int, a HUDAccount) {
			defer wg.Done()
			key := hudAccountKey(a)
			mu.Lock()
			entry, exists := cache[key]
			mu.Unlock()
			now := time.Now()
			floor := hudUsageFloor(a.Provider)
			maxAge := age
			if maxAge < floor {
				maxAge = floor
			}
			failureAge := maxAge / 2
			if failureAge < time.Minute {
				failureAge = time.Minute
			}
			if exists && !entry.FailAt.IsZero() && now.Sub(entry.FailAt) < failureAge {
				a = hudOverlay(entry.Account, a)
				a.Stale, a.Error = true, entry.Error
				out[i] = a
				return
			}
			if exists && now.Before(entry.RetryAt) {
				a = hudOverlay(entry.Account, a)
				a.Stale = true
				a.Error = "HTTP 429"
				out[i] = a
				return
			}
			if exists && !entry.FetchedAt.IsZero() && now.Sub(entry.FetchedAt) >= 0 && (now.Sub(entry.FetchedAt) < floor || !refresh && now.Sub(entry.FetchedAt) < maxAge) {
				a = hudOverlay(entry.Account, a)
				a.Stale = false
				a.Error = ""
				out[i] = a
				return
			}
			fetch, ok := fetchers[a.Provider]
			var value HUDAccount
			var err error
			if !ok {
				err = errors.New("unsupported provider")
			} else {
				value, err = fetch(ctx, a.Home)
			}
			if err != nil {
				if exists {
					a = hudOverlay(entry.Account, a)
				}
				a.Stale = true
				a.Error = hudSafeError(err)
				entry.Account = a
				entry.Account.Terminals = []string{}
				entry.Error = a.Error
				entry.RetryAt = time.Time{}
				var rate *hudRateLimitError
				if errors.As(err, &rate) {
					entry.FailAt = time.Time{}
					entry.RetryAt = rate.RetryAt
					if entry.RetryAt.After(now.Add(time.Hour)) {
						entry.RetryAt = now.Add(time.Hour)
					}
				} else {
					entry.FailAt = now
				}
				mu.Lock()
				updates[key] = entry
				mu.Unlock()
				out[i] = a
				return
			}
			a = hudOverlay(value, a)
			a.CheckedAt = time.Now()
			a.Stale = false
			a.Error = ""
			out[i] = a
			stored := a
			stored.Terminals = []string{}
			mu.Lock()
			updates[key] = hudCacheEntry{Account: stored, FetchedAt: a.CheckedAt}
			mu.Unlock()
		}(i, a)
	}
	wg.Wait()
	_ = hudMergeCache(updates)
	return out
}
func hudSafeError(err error) string {
	// Do not copy provider response bodies, credentials or account identifiers into
	// public JSON/cache. Fetchers return bounded errors without raw provider data.
	var rate *hudRateLimitError
	if errors.As(err, &rate) {
		return "HTTP 429"
	}
	return err.Error()
}
func hudSortCoupons(c *HUDCoupons) {
	sort.Slice(c.Items, func(i, j int) bool { return c.Items[i].ExpiresAt < c.Items[j].ExpiresAt })
	if len(c.Items) > 0 {
		c.NextExpiresAt = c.Items[0].ExpiresAt
	}
}

func hudUsageFloor(provider string) time.Duration {
	switch provider {
	case "codex":
		return 15 * time.Second
	case "claude":
		return 120 * time.Second
	case "gemini":
		return 300 * time.Second
	}
	return 0
}

// Ported from capacity-roller/main.go:17-152,159-167 (ranking/usable),
// runner.go:225-243 (first usable candidate), and store.go:49-59 (ignored key).
// Like cr, elapsed reset timestamps do not change registry remaining values.
// This read-only snapshot does not run runPreferred's network refresh probes.
func hudCRPlanWeight(provider, plan string) float64 {
	p := strings.ToLower(strings.TrimSpace(plan))
	if provider == "codex" {
		switch {
		case strings.Contains(p, "business"):
			if strings.Contains(p, "100") || strings.Contains(p, "5x") {
				return 4
			}
			return 3
		case strings.Contains(p, "enterprise") || strings.Contains(p, "edu"):
			return 0
		case strings.Contains(p, "pro") && strings.Contains(p, "lite"):
			return 3.5
		case strings.Contains(p, "pro") && strings.Contains(p, "20x"):
			return 5
		case strings.Contains(p, "pro"):
			return 4
		case strings.Contains(p, "plus"):
			return 3
		case strings.Contains(p, "go"):
			return 2
		case strings.Contains(p, "free"):
			return 1
		}
	} else {
		switch {
		case strings.Contains(p, "max") && strings.Contains(p, "20x"):
			return 4
		case strings.Contains(p, "max"):
			return 3
		case strings.Contains(p, "pro"):
			return 2
		case strings.Contains(p, "free"):
			return 1
		}
	}
	return 0
}

func hudCREffectivePlan(a hudRegistryAccount) string {
	if a.PlanOverride != "" {
		return a.PlanOverride
	}
	return a.Plan
}

func hudCRScoreWindow(w hudRegistryWindow) float64 {
	if w.Remaining == nil {
		return -1
	}
	return *w.Remaining
}

func hudCREffectiveWeight(r hudRegistry, a hudRegistryAccount) float64 {
	plan := hudCREffectivePlan(a)
	if n, ok := r.PlanWeights[a.Provider+":"+strings.ToLower(plan)]; ok {
		return float64(n)
	}
	return hudCRPlanWeight(a.Provider, plan)
}
func hudCRUsable(a hudRegistryAccount) bool {
	if a.State != "ready" {
		return false
	}
	if a.FiveHour.Remaining != nil && *a.FiveHour.Remaining <= 0 {
		return false
	}
	if a.Weekly.Remaining != nil && *a.Weekly.Remaining <= 0 {
		return false
	}
	return true
}
func hudCRSortedAccounts(r hudRegistry, provider string) []hudRegistryAccount {
	list := make([]hudRegistryAccount, 0)
	for _, a := range r.Accounts {
		if provider == "" || a.Provider == provider {
			list = append(list, a)
		}
	}
	mode := r.Priority
	if mode == "" {
		mode = "plan-high"
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if mode == "custom" && a.Rank != b.Rank {
			if a.Rank == 0 {
				return false
			}
			if b.Rank == 0 {
				return true
			}
			return a.Rank < b.Rank
		}
		if mode == "custom" {
			return hudCREmailLess(a.Email, b.Email)
		}
		if mode == "plan-high" || mode == "plan-low" {
			x, y := hudCREffectiveWeight(r, a), hudCREffectiveWeight(r, b)
			if x != y {
				if mode == "plan-high" {
					return x > y
				}
				return x < y
			}
		}
		if hudCRUsable(a) != hudCRUsable(b) {
			return hudCRUsable(a)
		}
		if r.Capacity != "neutral" && (a.Weekly.Remaining == nil) != (b.Weekly.Remaining == nil) {
			return a.Weekly.Remaining != nil
		}
		if r.Capacity != "neutral" && hudCRScoreWindow(a.Weekly) != hudCRScoreWindow(b.Weekly) {
			if r.Capacity == "least" && mode != "custom" {
				return hudCRScoreWindow(a.Weekly) < hudCRScoreWindow(b.Weekly)
			}
			return hudCRScoreWindow(a.Weekly) > hudCRScoreWindow(b.Weekly)
		}
		if r.Capacity != "neutral" && (a.FiveHour.Remaining == nil) != (b.FiveHour.Remaining == nil) {
			return a.FiveHour.Remaining != nil
		}
		if r.Capacity != "neutral" && hudCRScoreWindow(a.FiveHour) != hudCRScoreWindow(b.FiveHour) {
			if r.Capacity == "least" && mode != "custom" {
				return hudCRScoreWindow(a.FiveHour) < hudCRScoreWindow(b.FiveHour)
			}
			return hudCRScoreWindow(a.FiveHour) > hudCRScoreWindow(b.FiveHour)
		}
		return hudCREmailLess(a.Email, b.Email)
	})
	return list
}

func hudCREmailLess(a, b string) bool {
	aLocal, _, _ := strings.Cut(a, "@")
	bLocal, _, _ := strings.Cut(b, "@")
	if aLocal != bLocal {
		return aLocal < bLocal
	}
	return a < b
}

func hudCRPick(reg hudRegistry, provider string, now time.Time) (hudRegistryAccount, bool) {
	_ = now // cr's snapshot ranking does not adjust elapsed resets.
	for _, a := range hudCRSortedAccounts(reg, provider) {
		if contains1(reg.Ignored, a.Provider+":"+strings.ToLower(strings.TrimSpace(a.Email))) {
			continue
		}
		if hudCRUsable(a) {
			return a, true
		}
	}
	return hudRegistryAccount{}, false
}
func hudDiscoverSelected(now time.Time) ([]HUDAccount, error) {
	r, err := hudReadRegistry()
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	accounts := []HUDAccount{}
	for _, provider := range []string{"codex", "claude"} {
		if a, ok := hudCRPick(r, provider, now); ok {
			identity := hudAccount(provider, a.Home, r)
			identity.Selected = true
			accounts = append(accounts, identity)
		}
	}
	return accounts, nil
}
