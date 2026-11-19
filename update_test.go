package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Never call the real launchctl, and never let a test-built binary start a real update.
func init() {
	launchctlRun = func(args ...string) error { return nil }
	os.Setenv("ORCH_AUTO_UPDATE", "0")
}

func updateTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// updateFixture returns a clone that is one commit behind its bare origin.
func updateFixture(t *testing.T) (clone, upstream string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	updateTestGit(t, root, "init", "--quiet", "--bare", "-b", "main", bare)
	upstream = filepath.Join(root, "upstream")
	updateTestGit(t, root, "clone", "--quiet", bare, upstream)
	updateTestGit(t, upstream, "checkout", "--quiet", "-b", "main")
	os.WriteFile(filepath.Join(upstream, "a.txt"), []byte("1\n"), 0600)
	updateTestGit(t, upstream, "add", ".")
	updateTestGit(t, upstream, "commit", "--quiet", "-m", "one")
	updateTestGit(t, upstream, "push", "--quiet", "origin", "main")
	clone = filepath.Join(root, "clone")
	updateTestGit(t, root, "clone", "--quiet", bare, clone)
	os.WriteFile(filepath.Join(upstream, "a.txt"), []byte("2\n"), 0600)
	updateTestGit(t, upstream, "commit", "--quiet", "-am", "two")
	updateTestGit(t, upstream, "push", "--quiet", "origin", "main")
	updateLiveWriters = func(string) int { return 0 }
	t.Cleanup(func() { updateLiveWriters = func(root string) int { return len(liveWritersIn(root, nil)) } })
	return clone, upstream
}

func writeUpdateConfig(t *testing.T, repo, install string) {
	t.Helper()
	os.MkdirAll(orchHome(), 0700)
	cfg := `{"repos":[{"name":"orch","path":"` + repo + `","install":"` + install + `"}]}`
	if err := os.WriteFile(updateConfigPath(), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateFastForwardsAndRunsInstall(t *testing.T) {
	clone, upstream := updateFixture(t)
	writeUpdateConfig(t, clone, "touch installed.marker")
	var out bytes.Buffer
	if err := runUpdate(context.Background(), updateOptions{Out: &out}); err != nil {
		t.Fatal(err, out.String())
	}
	if got, want := updateTestGit(t, clone, "rev-parse", "HEAD"), updateTestGit(t, upstream, "rev-parse", "HEAD"); got != want {
		t.Fatalf("not fast-forwarded: %s != %s (%s)", got, want, out.String())
	}
	if !fileExists(filepath.Join(clone, "installed.marker")) {
		t.Fatal("install command did not run in the checkout")
	}
	log, _ := os.ReadFile(updateLogPath())
	if !strings.Contains(string(log), "orch: updated ") {
		t.Fatalf("log: %s", log)
	}
	if !fileExists(updateStampPath()) {
		t.Fatal("stamp not written")
	}
}

func TestUpdateDryRunLeavesCheckout(t *testing.T) {
	clone, _ := updateFixture(t)
	writeUpdateConfig(t, clone, "touch installed.marker")
	before := updateTestGit(t, clone, "rev-parse", "HEAD")
	var out bytes.Buffer
	if err := runUpdate(context.Background(), updateOptions{DryRun: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if updateTestGit(t, clone, "rev-parse", "HEAD") != before || fileExists(filepath.Join(clone, "installed.marker")) {
		t.Fatal("dry run changed the checkout")
	}
	if !strings.Contains(out.String(), "would fetch origin/main") {
		t.Fatal(out.String())
	}
}

func TestUpdateSkips(t *testing.T) {
	cases := []struct {
		name, want string
		prepare    func(t *testing.T, clone string)
	}{
		{"dirty", "skip: working tree is dirty", func(t *testing.T, clone string) {
			os.WriteFile(filepath.Join(clone, "a.txt"), []byte("local\n"), 0600)
		}},
		{"diverged", "skip: diverged", func(t *testing.T, clone string) {
			os.WriteFile(filepath.Join(clone, "b.txt"), []byte("x\n"), 0600)
			updateTestGit(t, clone, "add", ".")
			updateTestGit(t, clone, "commit", "--quiet", "-m", "local")
		}},
		{"branch", "skip: on branch \"topic\"", func(t *testing.T, clone string) {
			updateTestGit(t, clone, "checkout", "--quiet", "-b", "topic")
		}},
		{"live writer", "skip: live orch writer", func(t *testing.T, clone string) {
			updateLiveWriters = func(string) int { return 1 }
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clone, _ := updateFixture(t)
			c.prepare(t, clone)
			writeUpdateConfig(t, clone, "touch installed.marker")
			before := updateTestGit(t, clone, "rev-parse", "HEAD")
			var out bytes.Buffer
			if err := runUpdate(context.Background(), updateOptions{Out: &out}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), c.want) {
				t.Fatalf("want %q in %q", c.want, out.String())
			}
			if updateTestGit(t, clone, "rev-parse", "HEAD") != before || fileExists(filepath.Join(clone, "installed.marker")) {
				t.Fatal("skipped checkout was changed")
			}
		})
	}
}

func TestUpdateAheadOnlySkips(t *testing.T) {
	clone, _ := updateFixture(t)
	updateTestGit(t, clone, "pull", "--quiet", "--ff-only")
	os.WriteFile(filepath.Join(clone, "b.txt"), []byte("x\n"), 0600)
	updateTestGit(t, clone, "add", ".")
	updateTestGit(t, clone, "commit", "--quiet", "-m", "local")
	writeUpdateConfig(t, clone, "")
	var out bytes.Buffer
	if err := runUpdate(context.Background(), updateOptions{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skip: ahead of origin/main by 1") {
		t.Fatal(out.String())
	}
}

func TestUpdateSecondRunSkipsWhileLocked(t *testing.T) {
	clone, _ := updateFixture(t)
	writeUpdateConfig(t, clone, "touch installed.marker")
	lock, err := diskLock(updateLockPath(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockDisk(lock)
	before := updateTestGit(t, clone, "rev-parse", "HEAD")
	var out bytes.Buffer
	if err := runUpdate(context.Background(), updateOptions{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "another orch update is running") || updateTestGit(t, clone, "rev-parse", "HEAD") != before {
		t.Fatal(out.String())
	}
}

func TestUpdateModelsRunsRefreshEvenWhenCheckoutSkipped(t *testing.T) {
	clone, _ := updateFixture(t)
	os.WriteFile(filepath.Join(clone, "a.txt"), []byte("local\n"), 0600)
	writeUpdateConfig(t, clone, "")
	var got []string
	orig := updateRefreshModels
	updateRefreshModels = func(_ context.Context, o modelsRefreshOptions) error { got = append(got, o.Repo); return nil }
	t.Cleanup(func() { updateRefreshModels = orig })
	var out bytes.Buffer
	if err := runUpdate(context.Background(), updateOptions{Models: true, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != clone || !strings.Contains(out.String(), "orch: skip: working tree is dirty") || !strings.Contains(out.String(), "models: done") {
		t.Fatalf("refresh calls %v, out %s", got, out.String())
	}
}

func TestAutoUpdateTriggerConditions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	spawned := 0
	orig := autoUpdateSpawn
	autoUpdateSpawn = func() error { spawned++; return nil }
	t.Cleanup(func() { autoUpdateSpawn = orig })

	t.Setenv("ORCH_AUTO_UPDATE", "0")
	if maybeAutoUpdate([]string{"status"}) {
		t.Fatal("disabled trigger fired")
	}
	t.Setenv("ORCH_AUTO_UPDATE", "")
	t.Setenv("ORCH_ROLE", "worker")
	if maybeAutoUpdate([]string{"status"}) {
		t.Fatal("worker trigger fired")
	}
	t.Setenv("ORCH_ROLE", "")
	if maybeAutoUpdate([]string{"update", "--quiet"}) || maybeAutoUpdate([]string{"__task"}) {
		t.Fatal("update or internal command triggered itself")
	}
	if !maybeAutoUpdate([]string{"hook", "claude"}) || spawned != 1 {
		t.Fatal("missing stamp did not trigger")
	}
	if maybeAutoUpdate([]string{"status"}) || spawned != 1 {
		t.Fatal("fresh stamp triggered again")
	}
	old := time.Now().Add(-7 * time.Hour)
	os.Chtimes(updateStampPath(), old, old)
	if !maybeAutoUpdate([]string{"status"}) || spawned != 2 {
		t.Fatal("stale stamp did not trigger")
	}
	t.Setenv("ORCH_AUTO_UPDATE_INTERVAL", "1h")
	old = time.Now().Add(-2 * time.Hour)
	os.Chtimes(updateStampPath(), old, old)
	if !maybeAutoUpdate([]string{"status"}) || spawned != 3 {
		t.Fatal("interval override ignored")
	}
}

func TestInstallLaunchAgentDarwin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	var calls []string
	origRun, origOS := launchctlRun, launchdGOOS
	launchctlRun = func(args ...string) error { calls = append(calls, args[0]); return nil }
	launchdGOOS = "darwin"
	t.Cleanup(func() { launchctlRun, launchdGOOS = origRun, origOS })
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, "skill"), 0700)
	os.WriteFile(filepath.Join(repo, "skill", "SKILL.md"), []byte("skill"), 0600)
	if _, err := install(repo, true); err != nil {
		t.Fatal(err)
	}
	if fileExists(launchAgentPath()) || len(calls) != 0 {
		t.Fatal("dry run wrote the launch agent")
	}
	if _, err := install(repo, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(launchAgentPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>update</string>", "<string>--models</string>", "<key>Hour</key>\n\t\t<integer>3</integer>", "update.launchd.log"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("plist lacks %q:\n%s", want, data)
		}
	}
	if strings.Join(calls, ",") != "bootout,bootstrap" {
		t.Fatalf("launchctl calls: %v", calls)
	}
	calls = nil
	if _, err := install(repo, false); err != nil || strings.Join(calls, ",") != "print" {
		t.Fatal("reinstall re-registered a loaded, unchanged agent", err, calls)
	}
	rep := &installReport{}
	if err := uninstallLaunchAgent(rep, true); err != nil {
		t.Fatal(err)
	}
	if fileExists(launchAgentPath()) {
		t.Fatal("uninstall left the launch agent")
	}
}

func TestInstallLaunchAgentPreservesForeignAndSkipsOtherOS(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORCH_HOME", filepath.Join(home(), ".orch"))
	origOS := launchdGOOS
	t.Cleanup(func() { launchdGOOS = origOS })
	launchdGOOS = "linux"
	rep := &installReport{}
	if err := installLaunchAgent(rep, "/bin/orch", false, func(string) error { return nil }); err != nil || fileExists(launchAgentPath()) {
		t.Fatal("non-macOS wrote a launch agent", err)
	}
	launchdGOOS = "darwin"
	os.MkdirAll(filepath.Dir(launchAgentPath()), 0700)
	os.WriteFile(launchAgentPath(), []byte("foreign"), 0600)
	if err := installLaunchAgent(rep, "/bin/orch", false, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := uninstallLaunchAgent(rep, true); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(launchAgentPath()); string(data) != "foreign" {
		t.Fatal("foreign launch agent changed")
	}
}
