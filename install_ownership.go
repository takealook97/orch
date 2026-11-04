package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type installOwnership struct {
	Version int    `json:"version"`
	Repo    string `json:"repo"`
	Bin     string `json:"bin"`
	Block   string `json:"block"`
}

func ownershipPath() string { return filepath.Join(orchHome(), "install-ownership.json") }
func loadOwnership() (*installOwnership, error) {
	b, e := os.ReadFile(ownershipPath())
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var m installOwnership
	if e = json.Unmarshal(b, &m); e != nil {
		return nil, fmt.Errorf("malformed install ownership; preserve and repair %s: %w", ownershipPath(), e)
	}
	if m.Version != 1 || !filepath.IsAbs(m.Repo) || !filepath.IsAbs(m.Bin) || m.Block == "" {
		return nil, fmt.Errorf("invalid install ownership: %s", ownershipPath())
	}
	return &m, nil
}
func saveOwnership(repo, bin string) error {
	if e := os.MkdirAll(orchHome(), 0700); e != nil {
		return e
	}
	b, _ := json.MarshalIndent(installOwnership{1, repo, bin, constitutionBlock}, "", "  ")
	return atomicStateWrite(ownershipPath(), append(b, '\n'))
}
func hookOwned(command string, owners []string) bool {
	if len(owners) == 0 {
		return ownHook.MatchString(command)
	}
	return contains1(owners, command)
}
