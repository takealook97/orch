package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/shirou/gopsutil/v4/cpu"
	"strconv"
	"strings"
	"time"
)

// Operational errors remain in JSON so mods can retain their last good values.
func cmdHud(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: orch hud sys|usage|panes [--terminals t1,t2 | --selected] [--max-age 120s] [--refresh]")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var out any
	switch args[0] {
	case "sys":
		if len(args) != 1 {
			return usage()
		}
		stats, err := hudSys(ctx)
		if err != nil {
			stats.Error = err.Error()
		}
		out = stats
	case "panes":
		if len(args) != 1 {
			return usage()
		}
		out = hudPanes(ctx)
	case "usage":
		fs := flag.NewFlagSet("orch hud usage", flag.ContinueOnError)
		fs.SetOutput(stderr)
		selected := fs.Bool("selected", false, "show CR current account selections")
		terminals := fs.String("terminals", "", "comma-separated terminal addresses")
		age := fs.Duration("max-age", 120*time.Second, "cache freshness")
		refresh := fs.Bool("refresh", false, "refresh usage")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *age < 0 {
			return usage()
		}
		specified := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "terminals" {
				specified = true
			}
		})
		if *selected && specified {
			return usage()
		}
		out = hudUsageMode(ctx, *terminals, specified, *age, *refresh, *selected)
	default:
		return usage()
	}
	if err := json.NewEncoder(stdout).Encode(out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

type SysStats struct {
	At       time.Time `json:"at"`
	CPU      float64   `json:"cpu_pct"`
	MemUsed  uint64    `json:"mem_used_bytes"`
	MemTotal uint64    `json:"mem_total_bytes"`
	Error    string    `json:"error,omitempty"`
}

// cpuSample contains cumulative CPU seconds, never an average since boot.
type cpuSample struct {
	At     time.Time `json:"at"`
	User   float64   `json:"user"`
	System float64   `json:"system"`
	Idle   float64   `json:"idle"`
	Nice   float64   `json:"nice"`
}

func cpuPercent(prev, cur cpuSample) float64 {
	if prev.At.IsZero() {
		return 0
	}
	user, system, idle, nice := cur.User-prev.User, cur.System-prev.System, cur.Idle-prev.Idle, cur.Nice-prev.Nice
	total := user + system + idle + nice
	if user < 0 || system < 0 || idle < 0 || nice < 0 || total <= 0 {
		return 0
	}
	value := (user + system + nice) / total * 100
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Max(0, math.Min(100, value))
}

func hudCPUSample(ctx context.Context) (cpuSample, error) {
	ticks, err := cpu.TimesWithContext(ctx, false)
	if err != nil {
		return cpuSample{}, err
	}
	if len(ticks) == 0 {
		return cpuSample{}, fmt.Errorf("missing CPU ticks")
	}
	t := ticks[0]
	return cpuSample{At: time.Now(), User: t.User, System: t.System, Idle: t.Idle, Nice: t.Nice}, nil
}

func hudWriteCPUSample(path string, sample cpuSample) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".hud-cpu-*") // CreateTemp uses 0600.
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

// parseVMStat subtracts reclaimable purgeable and file-backed pages, matching
// Activity Monitor/Stats rather than counting all resident memory as used.
func parseVMStat(raw string) (uint64, error) {
	lines := strings.Split(raw, "\n")
	var pageSize uint64
	if len(lines) == 0 {
		return 0, fmt.Errorf("vm_stat: missing header")
	}
	if _, err := fmt.Sscanf(lines[0], "Mach Virtual Memory Statistics: (page size of %d bytes)", &pageSize); err != nil || pageSize == 0 {
		return 0, fmt.Errorf("vm_stat: invalid page size")
	}
	pages := map[string]int64{}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(value), "."), 10, 64)
		if err == nil && n >= 0 {
			pages[key] = n
		}
	}
	var used int64
	for _, key := range []string{"Pages active", "Pages inactive", "Pages speculative", "Pages wired down", "Pages occupied by compressor", "Pages purgeable", "File-backed pages"} {
		n, ok := pages[key]
		if !ok {
			return 0, fmt.Errorf("vm_stat: missing %s", key)
		}
		if key == "Pages purgeable" || key == "File-backed pages" {
			used -= n
		} else {
			used += n
		}
	}
	if used < 0 {
		return 0, nil
	}
	return uint64(used) * pageSize, nil
}

func hudSys(ctx context.Context) (SysStats, error) {
	s := SysStats{At: time.Now()}
	path := filepath.Join(orchHome(), "cache", "hud-cpu.json")
	var prev cpuSample
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &prev)
	}
	cur, err := hudCPUSample(ctx)
	if err != nil {
		return s, fmt.Errorf("CPU ticks: %w", err)
	}
	age := cur.At.Sub(prev.At)
	if prev.At.IsZero() || age < 200*time.Millisecond || age > 10*time.Second {
		prev = cur
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		case <-timer.C:
		}
		cur, err = hudCPUSample(ctx)
		if err != nil {
			return s, fmt.Errorf("CPU ticks: %w", err)
		}
	}
	s.CPU = cpuPercent(prev, cur)
	// Cache failure must not discard a valid live measurement.
	_ = hudWriteCPUSample(path, cur)
	raw, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return s, fmt.Errorf("sysctl: %w", err)
	}
	s.MemTotal, err = strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return s, fmt.Errorf("sysctl: %w", err)
	}
	raw, err = exec.CommandContext(ctx, "vm_stat").Output()
	if err != nil {
		return s, fmt.Errorf("vm_stat: %w", err)
	}
	s.MemUsed, err = parseVMStat(string(raw))
	if s.MemUsed > s.MemTotal {
		s.MemUsed = s.MemTotal
	}
	return s, err
}
