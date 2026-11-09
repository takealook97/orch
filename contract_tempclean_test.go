package main

import (
	"strings"
	"testing"
)

const noTempCleanupRule = "Leave temporary directories in place; never run `rm -rf`, especially on paths built from command substitution — orch cannot answer an approval dialog."

func assertNoTempCleanup(t *testing.T, body string) {
	t.Helper()
	if err := validateContract(body); err != nil {
		t.Fatal(err)
	}
	_, permissions, ok := strings.Cut(body, "## Permissions\n")
	if !ok {
		t.Fatal("missing permissions")
	}
	permissions, _, _ = strings.Cut(permissions, "\n## ")
	if !strings.Contains(permissions, "- "+noTempCleanupRule) {
		t.Fatal("permissions omit exact temporary cleanup rule")
	}
}

func TestContractNoTempCleanupRoles(t *testing.T) {
	for _, role := range roles {
		t.Run(role, func(t *testing.T) {
			r, _ := reviewFixture(t)
			assertNoTempCleanup(t, contractTemplate(r, "worker", role, "codex-high-l"))
		})
	}
}

func TestContractNoTempCleanupReviews(t *testing.T) {
	for _, role := range []string{"verify-critical", "verify-neutral", "verify-adversarial"} {
		t.Run(role, func(t *testing.T) {
			r, _ := reviewFixture(t)
			body, err := reviewContract(r, "reviewer", role, "codex-high-l", reviewContractOptions{Reviews: "author", Diff: "HEAD~1..HEAD", Focus: "edge cases"})
			if err != nil {
				t.Fatal(err)
			}
			assertNoTempCleanup(t, body)
		})
	}
}
