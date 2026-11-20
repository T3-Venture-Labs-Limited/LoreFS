package update

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo makes a small "origin" repository whose build and installer only
// write down that they ran, and a copy of it as a computer would have.
func repo(t *testing.T) (origin, clone string) {
	t.Helper()
	for _, tool := range []string{"git", "make", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	base := t.TempDir()
	origin, clone = filepath.Join(base, "origin"), filepath.Join(base, "clone")
	os.MkdirAll(origin, 0o755)
	run(t, origin, "git", "init", "-q", "-b", "main")
	write(t, origin, "Makefile", "build:\n\t@echo built >> build.log\n")
	write(t, origin, "install.sh", "#!/bin/sh\necho installed >> install.log\n")
	os.Chmod(filepath.Join(origin, "install.sh"), 0o755)
	write(t, origin, ".gitignore", "*.log\n")
	commit(t, origin, "first version")
	run(t, base, "git", "clone", "-q", origin, clone)
	return origin, clone
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, msg string) string {
	t.Helper()
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "-c", "commit.gpgsign=false", "commit", "-q", "-m", msg)
	return run(t, dir, "git", "rev-parse", "--short", "HEAD")
}

func ran(dir, log string) int {
	data, _ := os.ReadFile(filepath.Join(dir, log))
	return strings.Count(string(data), "\n")
}

func TestNothingNewDoesNothing(t *testing.T) {
	_, clone := repo(t)
	head := run(t, clone, "git", "rev-parse", "--short", "HEAD")
	res, err := Run(Options{Dir: clone, Installed: head, Out: &bytes.Buffer{}})
	if err != nil || res.Installed || len(res.Changes) != 0 {
		t.Fatalf("expected nothing to do: %+v, %v", res, err)
	}
	if ran(clone, "build.log") != 0 {
		t.Fatal("the build ran although nothing was new")
	}
}

func TestInstalledOlderThanTheCopyIsInstalled(t *testing.T) {
	_, clone := repo(t)
	res, err := Run(Options{Dir: clone, Installed: "0000000", Out: &bytes.Buffer{}})
	if err != nil || !res.Installed || ran(clone, "build.log") != 1 || ran(clone, "install.log") != 1 {
		t.Fatalf("the copy's version should have been built and installed: %+v, %v", res, err)
	}
}

func TestNewChangesAreFetchedBuiltAndInstalled(t *testing.T) {
	origin, clone := repo(t)
	old := run(t, clone, "git", "rev-parse", "--short", "HEAD")
	write(t, origin, "notes.txt", "one\n")
	commit(t, origin, "second version")
	write(t, origin, "notes.txt", "two\n")
	newest := commit(t, origin, "third version")

	// --check reports and changes nothing.
	res, err := Run(Options{Dir: clone, Installed: old, CheckOnly: true, Out: &bytes.Buffer{}})
	if err != nil || len(res.Changes) != 2 || res.Installed || res.To != newest {
		t.Fatalf("check: %+v, %v", res, err)
	}
	if got := run(t, clone, "git", "rev-parse", "--short", "HEAD"); got != old || ran(clone, "build.log") != 0 {
		t.Fatalf("check must not change anything (at %s)", got)
	}

	res, err = Run(Options{Dir: clone, Installed: old, Out: &bytes.Buffer{}})
	if err != nil || !res.Installed || res.To != newest || len(res.Changes) != 2 {
		t.Fatalf("update: %+v, %v", res, err)
	}
	if !strings.Contains(res.Changes[0], "third version") {
		t.Fatalf("newest change first, got %q", res.Changes)
	}
	if ran(clone, "build.log") != 1 || ran(clone, "install.log") != 1 {
		t.Fatal("build and installer should each have run once")
	}
}

// The first computers were told to `git checkout <version>`, which
// leaves the copy on no branch; a plain `git pull` fails there.
func TestACopyPinnedToOneVersionMovesToTheMainBranch(t *testing.T) {
	origin, clone := repo(t)
	old := run(t, clone, "git", "rev-parse", "--short", "HEAD")
	run(t, clone, "git", "checkout", "-q", old) // detached
	write(t, origin, "notes.txt", "one\n")
	newest := commit(t, origin, "second version")

	res, err := Run(Options{Dir: clone, Installed: old, Out: &bytes.Buffer{}})
	if err != nil || !res.Installed || res.To != newest {
		t.Fatalf("update from a pinned copy: %+v, %v", res, err)
	}
	if b := run(t, clone, "git", "symbolic-ref", "--short", "HEAD"); b != "main" {
		t.Fatalf("the copy should be on main now, is on %q", b)
	}
}

func TestACopyWithItsOwnConflictingChangesIsLeftAlone(t *testing.T) {
	origin, clone := repo(t)
	old := run(t, clone, "git", "rev-parse", "--short", "HEAD")
	write(t, clone, "notes.txt", "mine\n")
	commit(t, clone, "a change of my own")
	write(t, origin, "notes.txt", "theirs\n")
	commit(t, origin, "second version")

	res, err := Run(Options{Dir: clone, Installed: old, Out: &bytes.Buffer{}})
	if err == nil || res.Installed || !strings.Contains(err.Error(), "left alone") {
		t.Fatalf("expected a refusal: %+v, %v", res, err)
	}
	if ran(clone, "build.log") != 0 {
		t.Fatal("nothing should have been built")
	}
}

func TestRememberAndSource(t *testing.T) {
	_, clone := repo(t)
	t.Setenv("LOREFS_CONFIG_DIR", filepath.Join(t.TempDir(), "config"))
	if _, err := Source(); !errors.Is(err, ErrNoSource) {
		t.Fatalf("nothing remembered yet: %v", err)
	}
	if err := Remember(clone); err != nil {
		t.Fatal(err)
	}
	if got, err := Source(); err != nil || got != clone {
		t.Fatalf("Source() = %q, %v", got, err)
	}
	os.RemoveAll(filepath.Join(clone, ".git"))
	if _, err := Source(); !errors.Is(err, ErrNoSource) {
		t.Fatalf("a copy that is gone is no source: %v", err)
	}
	if err := Remember(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SourceFile()); !os.IsNotExist(err) {
		t.Fatal("forgetting should remove the note")
	}
}
