package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The default Claude home stays empty: the provider uses the unscoped keychain
// service on macOS, and ~/.claude/.credentials.json on Linux.
func hudDefaultAccounts() []HUDAccount {
	home, _ := os.UserHomeDir()
	accounts := []HUDAccount{}
	codexHome := filepath.Join(home, ".codex")
	if _, err := hudCodexBinary(); err == nil {
		if st, err := os.Stat(filepath.Join(codexHome, "auth.json")); err == nil && !st.IsDir() {
			a := hudAccount("codex", codexHome, hudRegistry{})
			a.Label, a.Selected = hudDefaultCodexLabel(codexHome), true
			accounts = append(accounts, a)
		}
	}
	if hudHasClaudeBinary(home) {
		a := hudAccount("claude", "", hudRegistry{})
		a.Label, a.Selected = hudDefaultClaudeLabel(home), true
		accounts = append(accounts, a)
	}
	return accounts
}

func hudHasClaudeBinary(home string) bool {
	if _, err := exec.LookPath("claude"); err == nil {
		return true
	}
	for _, path := range []string{filepath.Join(home, ".local", "bin", "claude"), "/opt/homebrew/bin/claude", "/usr/local/bin/claude"} {
		if st, err := os.Stat(path); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			return true
		}
	}
	return false
}

// Decode only the local JWT payload to read its email claim. This is a display
// hint, not signature verification or authentication; no tokens leave here.
func hudDefaultCodexLabel(home string) string {
	raw, _ := os.ReadFile(filepath.Join(home, "auth.json"))
	var auth struct {
		Tokens struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil {
		return ""
	}
	parts := strings.Split(auth.Tokens.IDToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return strings.SplitN(claims.Email, "@", 2)[0]
}

func hudDefaultClaudeLabel(home string) string {
	raw, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var config struct {
		Account struct {
			Email string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return ""
	}
	return strings.SplitN(config.Account.Email, "@", 2)[0]
}
