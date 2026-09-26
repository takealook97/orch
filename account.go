package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Account selection is independent of any particular account manager.
//
//	same (default): pass the Head's own login variables to workers of the same provider.
//	hook:           run $ORCH_ACCOUNT_CMD <kind> <exclude-labels> and use its KEY=VALUE lines.
//	                LABEL=<id> names the account (for rotation and reports).
//
// Workers of another provider than the Head start with that CLI's default login.
type Account struct {
	Label string
	Env   []string
}

var providerVars = map[string][]string{
	"codex":  {"CODEX_HOME", "CR_CODEX_NO_DAEMON"},
	"claude": {"CLAUDE_CONFIG_DIR"},
	"agy":    {},
}

func sameAccount(kind string) Account {
	acc := Account{Label: "same"}
	for _, key := range providerVars[kind] {
		if v := os.Getenv(key); v != "" {
			acc.Env = append(acc.Env, key+"="+v)
		}
	}
	if len(acc.Env) == 0 {
		acc.Label = "default"
	}
	return acc
}

func hookAccount(kind string, exclude []string) (Account, error) {
	cmdline := envOrch("ACCOUNT_CMD")
	if cmdline == "" {
		return Account{}, fmt.Errorf("ORCH_ACCOUNT_CMD is not set")
	}
	cmd := exec.Command("sh", "-c", cmdline+` "$@"`, "orch-account", kind, strings.Join(exclude, ","))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Account{}, fmt.Errorf("ORCH_ACCOUNT_CMD failed: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	acc := Account{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		key, val, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		if key == "LABEL" {
			acc.Label = val
			continue
		}
		acc.Env = append(acc.Env, key+"="+val)
	}
	if acc.Label == "" {
		return Account{}, fmt.Errorf("ORCH_ACCOUNT_CMD returned no LABEL (no available account)")
	}
	for _, x := range exclude {
		if x == acc.Label {
			return Account{}, fmt.Errorf("ORCH_ACCOUNT_CMD returned excluded account %s (no other available account)", acc.Label)
		}
	}
	return acc, nil
}

func pickAccount(mode, kind string, exclude []string) (Account, error) {
	switch mode {
	case "", "same":
		return sameAccount(kind), nil
	case "hook", "best":
		return hookAccount(kind, exclude)
	}
	return Account{}, fmt.Errorf("unknown --account %q (same|hook)", mode)
}

func envHas(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}
