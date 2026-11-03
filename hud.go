package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"regexp"
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

var hudCPURe = regexp.MustCompile(`CPU usage:\s*[\d.]+% user,\s*[\d.]+% sys,\s*([\d.]+)% idle`)
var hudMemRe = regexp.MustCompile(`PhysMem:\s*([\d.]+)([KMGT]?)\s+used`)

func parseTopCPU(s string) (float64, bool) {
	m := hudCPURe.FindAllStringSubmatch(s, -1)
	if len(m) == 0 {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[len(m)-1][1], 64)
	return 100 - n, err == nil && n >= 0 && n <= 100
}
func parseTopMem(s string) (uint64, bool) {
	m := hudMemRe.FindAllStringSubmatch(s, -1)
	if len(m) == 0 {
		return 0, false
	}
	v := m[len(m)-1]
	n, err := strconv.ParseFloat(v[1], 64)
	if err != nil {
		return 0, false
	}
	powers := map[string]int{"": 0, "K": 1, "M": 2, "G": 3, "T": 4}
	for i := 0; i < powers[v[2]]; i++ {
		n *= 1024
	}
	return uint64(n), true
}
func hudSys(ctx context.Context) (SysStats, error) {
	s := SysStats{At: time.Now()}
	raw, err := exec.CommandContext(ctx, "top", "-l", "2", "-n", "0", "-s", "1").Output()
	if err != nil {
		return s, fmt.Errorf("top: %w", err)
	}
	var ok bool
	s.CPU, ok = parseTopCPU(string(raw))
	if !ok {
		return s, fmt.Errorf("top: missing CPU sample")
	}
	s.MemUsed, ok = parseTopMem(string(raw))
	if !ok {
		return s, fmt.Errorf("top: missing PhysMem sample")
	}
	raw, err = exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		s.MemUsed = 0
		return s, fmt.Errorf("sysctl: %w", err)
	}
	s.MemTotal, err = strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		s.MemUsed = 0
	}
	return s, err
}
