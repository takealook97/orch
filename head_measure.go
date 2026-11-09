package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var spawnHeadMeasure = func(exe string, args []string, log *os.File) error {
	input, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer input.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func stopHook(data []byte) int {
	if envOrch("ROLE") == "worker" || envOrch("AUTO_MEASURE") == "0" {
		return 0
	}
	var in struct {
		Session    string `json:"session_id"`
		Transcript string `json:"transcript_path"`
	}
	if json.Unmarshal(data, &in) != nil || in.Session == "" || !filepath.IsAbs(in.Transcript) {
		return 0
	}
	if os.MkdirAll(orchHome(), 0700) != nil {
		return 0
	}
	home, err := resolvedOrchHome()
	if err != nil {
		return 0
	}
	log, err := os.OpenFile(filepath.Join(home, "measure.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return 0
	}
	defer log.Close()
	exe, err := os.Executable()
	if err != nil {
		return 0
	}
	_ = spawnHeadMeasure(exe, []string{"__measure-head", "--session", in.Session, "--session-file", in.Transcript}, log)
	return 0
}

func measureHeadCommand(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("__measure-head", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	session := fs.String("session", "", "")
	file := fs.String("session-file", "", "")
	err := fs.Parse(args)
	if err != nil || fs.NArg() != 0 || *session == "" || !filepath.IsAbs(*file) {
		err = fmt.Errorf("requires --session and absolute --session-file")
	} else {
		// Reuse the public collector's validation, parsing, locking and dedupe path.
		err = collectUsageCommand([]string{"--provider", "claude", "--session", *session, "--session-file", *file, "--subject", "head", "--entity", "head", "--period", *session, "--phase", "session"}, io.Discard)
	}
	if err != nil {
		// Session comes from hook input; keep even invalid identities on one log line.
		safeSession := strings.Map(func(r rune) rune {
			if r <= ' ' || r == 127 {
				return '_'
			}
			return r
		}, *session)
		fmt.Fprintf(stderr, "%s measure-head session=%s error=%s\n", time.Now().Format(time.RFC3339), safeSession, strings.Join(strings.Fields(err.Error()), " "))
		return 1
	}
	return 0
}
