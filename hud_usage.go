package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
type hudRegistry struct {
	Accounts []struct {
		Provider string `json:"provider"`
		Home     string `json:"home"`
		Email    string `json:"email"`
		Plan     string `json:"plan"`
	} `json:"accounts"`
	Ignored []string `json:"ignored"`
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
			s := args(pid)
			if strings.Contains(s, "/codex") {
				return pid, "codex"
			}
			if strings.Contains(s, "claude") {
				return pid, "claude"
			}
		}
		for _, child := range children[pid] {
			queue = append(queue, child.PID)
		}
	}
	return 0, ""
}
func hudEnvValue(raw, key string) string {
	for _, f := range strings.Fields(raw) {
		if strings.HasPrefix(f, key+"=") {
			return strings.TrimPrefix(f, key+"=")
		}
	}
	return ""
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
	if strings.TrimSpace(terminals) == "" {
		return accounts, nil
	}
	raw, err := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,comm=").Output()
	if err != nil {
		return accounts, fmt.Errorf("ps: %w", err)
	}
	procs := hudParseProcesses(string(raw))
	var failures []string
	for _, address := range strings.Split(terminals, ",") {
		address = strings.TrimSpace(address)
		if address == "" {
			continue
		}
		if strings.Contains(address, "/") {
			failures = append(failures, "remote terminals unsupported")
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
		pid, provider := hudFindAgent(terminal.PID, procs, func(pid int) string {
			s, _ := exec.CommandContext(ctx, "ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
			return string(s)
		})
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
}
type hudFetchFunc func(context.Context, string) (HUDAccount, error)
type hudRateLimitError struct{ RetryAt time.Time }

func (e *hudRateLimitError) Error() string { return "HTTP 429" }
func hudUsage(ctx context.Context, terminals string, specified bool, age time.Duration, refresh bool) HUDUsage {
	out := HUDUsage{At: time.Now(), Accounts: []HUDAccount{}}
	accounts, err := hudDiscover(ctx, terminals, specified)
	if err != nil {
		out.Error = err.Error()
	}
	out.Accounts = hudCachedUsage(ctx, accounts, age, refresh, map[string]hudFetchFunc{"codex": fetchCodexUsage, "claude": fetchClaudeUsage})
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
			maxAge := age
			if a.Provider == "claude" && maxAge < 300*time.Second {
				maxAge = 300 * time.Second
			}
			if exists && now.Before(entry.RetryAt) {
				a = hudOverlay(entry.Account, a)
				a.Stale = true
				a.Error = "HTTP 429"
				out[i] = a
				return
			}
			if exists && !refresh && !entry.FetchedAt.IsZero() && now.Sub(entry.FetchedAt) >= 0 && now.Sub(entry.FetchedAt) < maxAge {
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
				var rate *hudRateLimitError
				if errors.As(err, &rate) {
					entry.Account = a
					entry.Account.Terminals = []string{}
					entry.RetryAt = rate.RetryAt
					mu.Lock()
					updates[key] = entry
					mu.Unlock()
				}
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
