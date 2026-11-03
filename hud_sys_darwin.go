package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func hudMemory(ctx context.Context) (used, total uint64, err error) {
	raw, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("sysctl: %w", err)
	}
	total, err = strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("sysctl: %w", err)
	}
	raw, err = exec.CommandContext(ctx, "vm_stat").Output()
	if err != nil {
		return 0, total, fmt.Errorf("vm_stat: %w", err)
	}
	used, err = parseVMStat(string(raw))
	if used > total {
		used = total
	}
	return used, total, err
}
