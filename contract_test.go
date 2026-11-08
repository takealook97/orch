package main

import (
	"strings"
	"testing"
)

func TestContractTemplateTempStorageGuidance(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	body := contractTemplate(&Run{ID: "storage-guidance"}, "impl", roleProduce, "codex-high-m")
	for _, guidance := range []string{"toolchains, virtualenvs, package/module caches and build outputs", "temp dir ($TMPDIR) outside ~/.orch", "store only small logs/excerpts under evidence/"} {
		if !strings.Contains(body, guidance) {
			t.Errorf("contract template lacks %q", guidance)
		}
	}
}
