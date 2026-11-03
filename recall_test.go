package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecallMatchingAndRanking(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	repo := taskRepo(t)
	other := taskRepo(t)
	add := func(id, cwd, body string, age time.Duration) {
		d := filepath.Join(runsDir(), id)
		os.MkdirAll(filepath.Join(d, "results"), 0700)
		b, _ := json.Marshal(map[string]any{"workers": []map[string]string{{"cwd": cwd, "result": filepath.Join(d, "results", "x.md")}}})
		os.WriteFile(filepath.Join(d, "run.json"), b, 0600)
		p := filepath.Join(d, "results", "x.md")
		os.WriteFile(p, []byte(body), 0600)
		os.Chtimes(p, time.Now().Add(age), time.Now().Add(age))
	}
	add("a", repo, "Alpha\nbeta\nalpha beta "+strings.Repeat("x", 200), -time.Hour)
	add("b", repo, "alpha beta", 0)
	add("c", other, "alpha beta\nalpha beta", 0)
	add("d", repo, "alpha only", 0)
	hits, err := recallSearch("ALPHA beta", repo, false, 10)
	if err != nil || len(hits) != 2 || !strings.Contains(hits[0].Path, "/a/") {
		t.Fatalf("%+v %v", hits, err)
	}
	for _, s := range hits[0].Lines {
		if len([]rune(s)) > 160 {
			t.Fatal("long snippet")
		}
	}
	hits, err = recallSearch("alpha beta", repo, true, 10)
	if err != nil || len(hits) != 3 {
		t.Fatalf("%+v %v", hits, err)
	}
	hits, _ = recallSearch("alpha beta", repo, true, 1)
	if len(hits) != 1 {
		t.Fatal("limit")
	}
}

func TestRecallKindsAndCommand(t *testing.T) {
	t.Setenv("ORCH_HOME", t.TempDir())
	t.Setenv("ORCH_ROLE", "worker")
	repo := taskRepo(t)
	d := filepath.Join(runsDir(), "kinds")
	td := filepath.Join(d, "tasks", "task-1")
	os.MkdirAll(td, 0700)
	task := Task{ID: "task-1", Kind: "ask", Cwd: repo}
	b, _ := json.Marshal(task)
	os.WriteFile(filepath.Join(td, "task.json"), b, 0600)
	os.WriteFile(filepath.Join(td, "answer.md"), []byte("needle answer"), 0600)
	for _, name := range []string{"knowledge.md", "findings.md"} {
		os.WriteFile(filepath.Join(d, name), []byte("needle knowledge"), 0600)
	}
	hits, err := recallSearch("needle", repo, false, 10)
	if err != nil || len(hits) != 3 {
		t.Fatalf("%+v %v", hits, err)
	}
	var out, errs bytes.Buffer
	if code := run([]string{"recall", "needle", "--all-repos", "--json"}, strings.NewReader(""), &out, &errs); code != 0 {
		t.Fatalf("%d %s", code, errs.String())
	}
	var got []recallHit
	if json.Unmarshal(out.Bytes(), &got) != nil || len(got) != 3 {
		t.Fatal(out.String())
	}
	if recallCommand([]string{"needle", "--limit", "0"}, &out, &errs) != 2 {
		t.Fatal("invalid limit")
	}
}
