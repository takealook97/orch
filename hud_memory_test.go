package main

import "testing"

func TestHUDMeminfo(t *testing.T) {
	used, total, err := parseHUDMeminfo("MemTotal:       16384000 kB\nMemFree: 1000 kB\nMemAvailable: 4096000 kB\nCached: 2000 kB\n")
	if err != nil || total != 16384000*1024 || used != (16384000-4096000)*1024 {
		t.Fatalf("%d %d %v", used, total, err)
	}
	for _, raw := range []string{"", "MemTotal: 100 kB", "MemTotal: 100 kB\nMemAvailable: 101 kB", "MemTotal: -1 kB\nMemAvailable: 0 kB", "MemTotal: 100 MB\nMemAvailable: 0 kB", "MemTotal: 18446744073709551615 kB\nMemAvailable: 0 kB"} {
		if _, _, err := parseHUDMeminfo(raw); err == nil {
			t.Fatalf("accepted invalid meminfo %q", raw)
		}
	}
}
