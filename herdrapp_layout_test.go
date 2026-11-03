package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func layoutNode(s string) any {
	var n any
	if err := json.Unmarshal([]byte(s), &n); err != nil {
		panic(err)
	}
	return n
}

const appHeadBlock = `{"type":"block","id":"ph","content":{"terminal":"th"},"extra":"preserved"}`
const appWorkerOne = `{"type":"block","id":"p1","content":{"terminal":"t1"}}`
const appWorkerTwo = `{"type":"block","id":"p2","content":{"terminal":"t2"}}`

func TestAppLayoutCanonicalTrees(t *testing.T) {
	appTest(t)
	hcRun = func(args ...string) ([]byte, error) { return nil, errHCNotFound }
	for _, tt := range []struct {
		name, root, head string
		order            []string
		foreign          bool
		retired          map[string]appIdentity
	}{
		{"flat", `{"type":"row","id":"tab","children":[` + appWorkerTwo + `,` + appHeadBlock + `,` + appWorkerOne + `]}`, "th", []string{"t1", "t2"}, false, nil},
		{"nested", `{"type":"column","id":"old","children":[` + appHeadBlock + `,{"type":"row","id":"mess","children":[` + appWorkerOne + `,` + appWorkerTwo + `]}]}`, "th", []string{"t1", "t2"}, false, nil},
		{"overflow", `{"type":"row","id":"tab","children":[` + appWorkerOne + `,` + appWorkerTwo + `]}`, "", []string{"t1", "t2"}, false, nil},
		{"foreign", `{"type":"row","id":"tab","children":[` + appHeadBlock + `,` + appWorkerOne + `]}`, "th", nil, true, nil},
		{"mod", `{"type":"row","id":"tab","children":[` + appHeadBlock + `,{"type":"block","id":"mod","content":{"mod":"editor"}}]}`, "th", nil, true, nil},
		{"head only", `{"type":"row","id":"tab","children":[` + appHeadBlock + `]}`, "th", nil, false, nil},
		{"weighted head", `{"type":"row","id":"tab","children":[{"type":"block","id":"ph","content":{"terminal":"th"},"extra":"preserved","size":{"weight":3.0}}]}`, "th", nil, false, nil},
		{"known live elsewhere", `{"type":"row","id":"tab","children":[` + appHeadBlock + `,` + appWorkerOne + `]}`, "th", nil, false, map[string]appIdentity{"t1": {"run", "worker"}}},
		// A just-closed worker's block lingers until the app collapses it; it is dropped, not foreign.
		{"retired block", `{"type":"row","id":"tab","children":[` + appHeadBlock + `,{"type":"column","id":"c","children":[` + appWorkerOne + `,` + appWorkerTwo + `]}]}`, "th", []string{"t2"}, false, map[string]appIdentity{"t1": {"run", "worker"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, err := canonicalAppRoot(layoutNode(tt.root), "tab", tt.head, tt.order, tt.retired)
			if tt.foreign {
				if err == nil || !strings.Contains(err.Error(), "I6") {
					t.Fatalf("expected I6: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(root)
			var normalized any
			_ = json.Unmarshal(encoded, &normalized)
			again, err := canonicalAppRoot(normalized, "tab", tt.head, tt.order, tt.retired)
			if err != nil || !reflect.DeepEqual(root, again) {
				t.Fatalf("not canonical: %s %v", encoded, err)
			}
			children := root["children"].([]any)
			if tt.head != "" {
				h := children[0].(map[string]any)
				if len(tt.order) == 0 && h["size"] != nil {
					t.Fatalf("lone head retains size: %v", h)
				}
				if h["extra"] != "preserved" {
					t.Fatal("lost leaf data")
				}
			}
			if len(tt.order) > 0 {
				col := children[len(children)-1].(map[string]any)
				if col["type"] != "column" || col["id"] != "orchcol-tab" {
					t.Fatalf("column=%v", col)
				}
				workers := col["children"].([]any)
				if leafTerminal(workers[0].(map[string]any)) != tt.order[0] || len(workers) != len(tt.order) {
					t.Fatal("spawn order lost")
				}
			}
		})
	}
}
func TestNormalizeAppLayoutRPC(t *testing.T) {
	for _, mode := range []string{"rewrite", "canonical", "foreign", "conflict", "conflict limit", "verify failure"} {
		t.Run(mode, func(t *testing.T) {
			old := hcRPC
			t.Cleanup(func() { hcRPC = old })
			root := layoutNode(`{"type":"row","id":"tab","children":[` + appHeadBlock + `,` + appWorkerOne + `,` + appWorkerTwo + `]}`)
			if mode == "canonical" {
				root, _ = canonicalAppRoot(root, "tab", "th", []string{"t1", "t2"}, nil)
			}
			if mode == "foreign" {
				root = layoutNode(`{"type":"row","id":"tab","children":[` + appHeadBlock + `,{"type":"block","id":"x","content":{"mod":"x"}}]}`)
			}
			writes := 0
			hcRPC = func(method string, params any) (json.RawMessage, error) {
				switch method {
				case "doc.get":
					return json.Marshal(map[string]any{"version": writes + 1, "value": map[string]any{"spaces": []any{map[string]any{"id": "space", "tabs": []any{map[string]any{"id": "tab", "root": root}}}}}})
				case "doc.set":
					writes++
					if mode == "conflict limit" || mode == "conflict" && writes == 1 {
						return nil, fmt.Errorf(`{"data":{"kind":"conflict"}}`)
					}
					p := params.(map[string]any)
					tab, _ := docTab(p["value"].(map[string]any), "space", "tab")
					if mode != "verify failure" {
						root = tab["root"]
					}
					return json.RawMessage(`{"version":5}`), nil
				}
				panic(method)
			}
			err := normalizeAppTab("space", "tab", "th", "", []string{"t1", "t2"}, nil)
			switch mode {
			case "foreign", "conflict limit", "verify failure":
				if err == nil {
					t.Fatal("expected error")
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			want := 1
			switch mode {
			case "canonical", "foreign":
				want = 0
			case "conflict":
				want = 2
			case "conflict limit":
				want = 3
			}
			if writes != want {
				t.Fatalf("writes=%d want %d", writes, want)
			}
		})
	}
}

func TestNormalizeAppTabNames(t *testing.T) {
	for _, tc := range []struct {
		name, head, desired string
		canonical           bool
		wantWrites          int
		wantName            string
	}{
		{"overflow layout", "", "orch run 1", false, 1, "orch run 1"},
		{"overflow name only", "", "orch run 1", true, 1, "orch run 1"},
		{"head untouched", "th", "", false, 0, "My Head"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := hcRPC
			t.Cleanup(func() { hcRPC = old })
			leaf := appWorkerOne
			order := []string{"t1"}
			if tc.head != "" {
				leaf = appHeadBlock
				order = nil
			}
			root := layoutNode(`{"type":"row","id":"tab","children":[` + leaf + `]}`)
			if tc.canonical {
				root, _ = canonicalAppRoot(root, "tab", tc.head, order, nil)
			}
			currentName := "My Head"
			writes := 0
			hcRPC = func(method string, params any) (json.RawMessage, error) {
				if method == "doc.get" {
					return json.Marshal(map[string]any{"version": writes + 1, "value": map[string]any{"spaces": []any{map[string]any{"id": "space", "tabs": []any{map[string]any{"id": "tab", "root": root, "name": currentName}}}}}})
				}
				writes++
				tab, err := docTab(params.(map[string]any)["value"].(map[string]any), "space", "tab")
				if err != nil {
					t.Fatal(err)
				}
				root = tab["root"]
				currentName = tab["name"].(string)
				return json.RawMessage(`{}`), nil
			}
			if err := normalizeAppTab("space", "tab", tc.head, tc.desired, order, nil); err != nil {
				t.Fatal(err)
			}
			if writes != tc.wantWrites || currentName != tc.wantName {
				t.Fatalf("writes=%d name=%q", writes, currentName)
			}
		})
	}
}
