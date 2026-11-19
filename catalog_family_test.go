package main

import (
	"strings"
	"testing"
)

const familyPinMsg = "계열 교체는 소유자 명시 승인 없이 금지"

func TestKeyFamiliesStayPinned(t *testing.T) {
	pinned := map[string][3]string{
		"codex-low-h":     {"Luna", "luna", "gpt-"},
		"codex-medium-m":  {"Terra", "terra", "gpt-"},
		"codex-high-l":    {"Sol", "sol", "gpt-"},
		"codex-high-m":    {"Sol", "sol", "gpt-"},
		"claude-high-m":   {"Opus", "opus", "opus-"},
		"claude-medium-m": {"Sonnet", "sonnet", "sonnet-"},
		"gemini-medium-m": {"Gemini", "gemini-flash", "gemini-"},
	}
	var m modelManifest
	if err := decodeModels(embeddedModels, &m); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, k := range m.Keys {
		want, ok := pinned[k.Key]
		if !ok {
			t.Errorf("%s: 고정 표에 없는 키. %s", k.Key, familyPinMsg)
			continue
		}
		seen[k.Key] = true
		if k.Tier != want[0] {
			t.Errorf("%s: tier %q, 고정값 %q. %s", k.Key, k.Tier, want[0], familyPinMsg)
		}
		if k.Model != want[1] {
			t.Errorf("%s: 별칭 %q, 고정값 %q. %s", k.Key, k.Model, want[1], familyPinMsg)
		}
		if id := m.Models[k.Model]; !strings.HasPrefix(id, want[2]) {
			t.Errorf("%s: 모델 ID %q 는 접두사 %q 가 아니다. %s", k.Key, id, want[2], familyPinMsg)
		}
	}
	for key := range pinned {
		if !seen[key] {
			t.Errorf("%s: models.yaml 에 없다. %s", key, familyPinMsg)
		}
	}
}

func TestNewestModelIDIgnoresOtherFamilies(t *testing.T) {
	list := []string{"gpt-9.0-astra", "gpt-9.0-terra", "gpt-6.3-sol", "gpt-9.0-sol-high", "fable-9.9-medium", "gemini-9-pro-medium"}
	for _, c := range []struct{ cur, want string }{
		{"gpt-6.2-sol", "gpt-6.3-sol"},
		{"gpt-6.1-terra", "gpt-9.0-terra"},
		{"gemini-3.8-flash-medium", ""},
	} {
		if got := newestModelID(c.cur, list); got != c.want {
			t.Errorf("newestModelID(%s) = %q, want %q (다른 계열 slug 는 버전이 높아도 고르지 않는다)", c.cur, got, c.want)
		}
	}
}
