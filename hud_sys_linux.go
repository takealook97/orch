package main

import (
	"context"
	"fmt"
	"os"
)

func hudMemory(ctx context.Context) (used, total uint64, err error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, fmt.Errorf("meminfo: %w", err)
	}
	return parseHUDMeminfo(string(raw))
}
