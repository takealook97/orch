package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// atomicStateWrite leaves the previous complete version intact on write failure.
func atomicStateWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Locks are separate from run.json's short read/modify/write lock.
func exclusiveFile(path string, wait bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	flags := syscall.LOCK_EX
	if !wait {
		flags |= syscall.LOCK_NB
	}
	if err = syscall.Flock(int(f.Fd()), flags); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s already in use or unavailable: %w", path, err)
	}
	return func() { f.Close() }, nil
}

var controlTimeout = 15 * time.Second

func controlDeadline(bin string, args []string) time.Duration {
	d := controlTimeout
	for i, a := range args {
		if a == "--timeout" && i+1 < len(args) {
			if n, e := strconv.ParseInt(args[i+1], 10, 64); e == nil && n > 0 && n < 86400000 {
				unit := time.Second
				if bin == "herdr" {
					unit = time.Millisecond
				}
				d = max(d, time.Duration(n)*unit+controlTimeout)
			}
		}
	}
	return d
}

// Cap captured output while continuing to drain it. Account errors must not echo secrets.
type controlBuffer struct {
	sync.Mutex
	b []byte
}

func (b *controlBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	n := min(len(p), max(0, 1024*1024-len(b.b)))
	b.b = append(b.b, p[:n]...)
	return len(p), nil
}
func (b *controlBuffer) bytes() []byte {
	b.Lock()
	defer b.Unlock()
	return append([]byte(nil), b.b...)
}
func boundedControl(bin string, args []string, env []string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, bin, args...)
	if env == nil {
		env = os.Environ()
	}
	c.Env = childCLIEnv(env)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = 100 * time.Millisecond
	var out, stderr controlBuffer
	c.Stdout = &out
	c.Stderr = &stderr
	err := c.Run()
	if c.Process != nil {
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	if err != nil {
		if bin != "sh" {
			return out.bytes(), fmt.Errorf("%s control command failed (deadline %s): %w: %s", bin, timeout, err, stderr.bytes())
		}
		return out.bytes(), fmt.Errorf("%s control command failed (deadline %s): %w", bin, timeout, err)
	}
	return out.bytes(), nil
}
