package main

import (
	"fmt"
	"strconv"
	"strings"
)

func parseHUDMeminfo(raw string) (used, total uint64, err error) {
	values := map[string]uint64{}
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || (key != "MemTotal" && key != "MemAvailable") {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) != 2 || fields[1] != "kB" {
			return 0, 0, fmt.Errorf("meminfo: invalid %s", key)
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil || n > ^uint64(0)/1024 {
			return 0, 0, fmt.Errorf("meminfo: invalid %s", key)
		}
		values[key] = n * 1024
	}
	total, hasTotal := values["MemTotal"]
	available, hasAvailable := values["MemAvailable"]
	if !hasTotal || !hasAvailable || total == 0 || available > total {
		return 0, 0, fmt.Errorf("meminfo: missing or inconsistent memory totals")
	}
	return total - available, total, nil
}
