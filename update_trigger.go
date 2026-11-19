package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func updateTriggerLockPath() string { return filepath.Join(orchHome(), "update.trigger") }

// autoUpdateSpawn is replaced in tests.
var autoUpdateSpawn = func() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "update", "--quiet")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(), "ORCH_AUTO_UPDATE=0")
	// Nil stdin/stdout/stderr connect to the null device.
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// autoUpdateExempt lists entry points that must not start a background update:
// update itself, models refresh (it may run inside update), stats and internal commands.
func autoUpdateExempt(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch {
	case args[0] == "update", args[0] == "stats", strings.HasPrefix(args[0], "__"):
		return true
	case args[0] == "models" && len(args) > 1 && args[1] == "refresh":
		return true
	}
	return false
}

func stampFresh(path string, now time.Time) bool {
	info, err := os.Stat(path)
	return err == nil && now.Sub(info.ModTime()) < autoUpdateEvery()
}

// maybeAutoUpdate launches a detached `orch update --quiet` when the stamp is stale.
// Common path (hooks included): environment reads and one stat. Only a stale stamp
// takes the non-blocking trigger lock, re-checks the stamp and spawns once.
func maybeAutoUpdate(args []string) bool {
	if envOrch("ROLE") == "worker" || envOrch("AUTO_UPDATE") == "0" || autoUpdateExempt(args) {
		return false
	}
	stamp := updateStampPath()
	if stampFresh(stamp, time.Now()) {
		return false
	}
	if os.MkdirAll(orchHome(), 0700) != nil {
		return false
	}
	lock, err := diskLock(updateTriggerLockPath(), true)
	if err != nil {
		return false
	}
	defer unlockDisk(lock)
	now := time.Now()
	if stampFresh(stamp, now) || !touchStamp(stamp, now) {
		return false
	}
	if autoUpdateSpawn() != nil {
		_ = os.Chtimes(stamp, time.Unix(0, 0), time.Unix(0, 0))
		return false
	}
	return true
}
