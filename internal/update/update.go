// Package update brings the lorefs program on this computer up to date
// from the copy of the repository it was installed from.
package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
)

// ErrNoSource: this computer was not installed from a copy of the
// repository (or the copy is gone), so there is nothing to update from.
var ErrNoSource = errors.New("no repository to update from")

// SourceFile holds the path of the repository copy this computer was
// installed from. It is kept with the settings, and `lorefs leave` does
// not remove it.
func SourceFile() string { return filepath.Join(config.Dir(), "source") }

// Remember records where the program was installed from. An empty dir
// forgets it (an install from a release file).
func Remember(dir string) error {
	if dir == "" {
		err := os.Remove(SourceFile())
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(SourceFile(), []byte(abs+"\n"), 0o600)
}

// Source returns the remembered repository copy, if it is still there.
func Source() (string, error) {
	data, err := os.ReadFile(SourceFile())
	if err != nil {
		return "", ErrNoSource
	}
	dir := strings.TrimSpace(string(data))
	if dir == "" {
		return "", ErrNoSource
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return "", fmt.Errorf("%w: %s is no longer a copy of the repository", ErrNoSource, dir)
	}
	return dir, nil
}

// Options control Run.
type Options struct {
	Dir       string    // the repository copy
	Installed string    // version of the program that is running
	CheckOnly bool      // only say whether there is something newer
	Out       io.Writer // progress, and the output of the build
}

// Result says what Run found and did.
type Result struct {
	From, To  string   // repository versions before and after
	Changes   []string // one line per new change, newest first
	Installed bool     // the build and the installer ran
}

// Run fetches the newest version, and unless CheckOnly is set, builds
// and installs it. It only ever moves forward: a repository copy with
// changes of its own that conflict is left alone, with git's message.
func Run(opt Options) (Result, error) {
	var res Result
	for _, tool := range []string{"git", "make", "go"} {
		if opt.CheckOnly && tool != "git" {
			continue
		}
		if _, err := exec.LookPath(tool); err != nil {
			return res, fmt.Errorf("updating needs the program %q, which is not installed", tool)
		}
	}
	g := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = opt.Dir
		out, err := cmd.CombinedOutput()
		text := strings.TrimSpace(string(out))
		if err != nil {
			return text, fmt.Errorf("git %s: %s", strings.Join(args, " "), firstNonEmpty(text, err.Error()))
		}
		return text, nil
	}
	if _, err := g("fetch", "--quiet", "origin"); err != nil {
		return res, fmt.Errorf("could not reach the repository: %w", err)
	}

	// Where the newest version is: what the current branch follows, or
	// the repository's main branch when the copy sits on one fixed
	// version (after `git checkout <version>`).
	branch, _ := g("symbolic-ref", "--quiet", "--short", "HEAD")
	target, err := g("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if branch == "" || err != nil {
		target, err = g("symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
		if err != nil {
			target = "origin/main"
		}
	}
	res.From, _ = g("describe", "--tags", "--always", "--dirty")
	if log, err := g("log", "--format=%h %s", "HEAD.."+target); err == nil && log != "" {
		res.Changes = strings.Split(log, "\n")
	}
	res.To = res.From
	if opt.CheckOnly {
		if len(res.Changes) > 0 {
			res.To, _ = g("rev-parse", "--short", target)
		}
		return res, nil
	}

	if len(res.Changes) > 0 {
		if branch == "" {
			// Leave the fixed version for the main branch.
			if _, err := g("checkout", "--quiet", strings.TrimPrefix(target, "origin/")); err != nil {
				return res, err
			}
		}
		if _, err := g("merge", "--ff-only", "--quiet", target); err != nil {
			return res, fmt.Errorf("the repository copy at %s has changes of its own, so it was left alone: %w", opt.Dir, err)
		}
		res.To, _ = g("describe", "--tags", "--always", "--dirty")
	}
	if len(res.Changes) == 0 && opt.Installed == res.From {
		return res, nil // nothing new, and what is installed is what is here
	}

	for _, step := range [][]string{{"make", "build"}, {"./install.sh"}} {
		cmd := exec.Command(step[0], step[1:]...)
		cmd.Dir = opt.Dir
		cmd.Stdout, cmd.Stderr = opt.Out, opt.Out
		if err := cmd.Run(); err != nil {
			return res, fmt.Errorf("%s failed (%v); the installed program was not changed by that step", strings.Join(step, " "), err)
		}
	}
	res.Installed = true
	return res, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
