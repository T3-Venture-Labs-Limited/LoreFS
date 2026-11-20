// Package ui holds what the lorefs command shows to a person: version
// lists, what changed between versions, and the interactive history view.
package ui

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/status"
)

// IsTerminal reports whether f is an interactive terminal.
func IsTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// When renders a time for a person: relative when recent, a date otherwise.
func When(t, now time.Time) string {
	local := t.Local()
	d := now.Sub(t)
	switch {
	case d < 48*time.Hour:
		return fmt.Sprintf("%s (%s)", local.Format("Mon 15:04"), status.Ago(d))
	default:
		return local.Format("2 Jan 2006 15:04")
	}
}

// VersionLine is one row of a version list.
func VersionLine(n int, v history.Version, now time.Time) string {
	return fmt.Sprintf("%3d  %-34s %-14s %10s", n, When(v.Time, now), v.Machine, status.Size(v.Size))
}

// IsText reports whether a file looks like text a person can read as a
// line-by-line comparison.
func IsText(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, _ := io.ReadFull(f, buf)
	buf = buf[:n]
	if bytes.IndexByte(buf, 0) >= 0 {
		return false
	}
	// A multi-byte character may be cut at the end of the sample.
	for i := 0; i < 4 && len(buf) > 0 && !utf8.Valid(buf); i++ {
		buf = buf[:len(buf)-1]
	}
	return utf8.Valid(buf)
}

// Diff writes what changed from the file at oldPath to the file at
// newPath. For text it shows added and removed lines; otherwise sizes.
func Diff(w io.Writer, oldPath, oldLabel, newPath, newLabel string, color bool) error {
	oldSt, oldErr := os.Stat(oldPath)
	newSt, newErr := os.Stat(newPath)
	if oldErr != nil {
		return oldErr
	}
	if newErr != nil {
		fmt.Fprintf(w, "%s no longer exists. %s holds %s.\n", newLabel, oldLabel, status.Size(oldSt.Size()))
		return nil
	}
	if !IsText(oldPath) || !IsText(newPath) {
		fmt.Fprintf(w, "This is not a text file, so the content cannot be compared line by line.\n\n")
		fmt.Fprintf(w, "  %-28s %10s   %s\n", oldLabel, status.Size(oldSt.Size()), oldSt.ModTime().Local().Format("2 Jan 2006 15:04:05"))
		fmt.Fprintf(w, "  %-28s %10s   %s\n", newLabel, status.Size(newSt.Size()), newSt.ModTime().Local().Format("2 Jan 2006 15:04:05"))
		return nil
	}
	cmd := exec.Command("diff", "-u", "--label", oldLabel, "--label", newLabel, oldPath, newPath)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			return fmt.Errorf("comparing the two versions: %w", err)
		}
	}
	if len(out) == 0 {
		fmt.Fprintln(w, "The content is the same.")
		return nil
	}
	if !color {
		_, err = w.Write(out)
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			fmt.Fprintf(w, "\x1b[1m%s\x1b[0m\n", line)
		case strings.HasPrefix(line, "+"):
			fmt.Fprintf(w, "\x1b[32m%s\x1b[0m\n", line)
		case strings.HasPrefix(line, "-"):
			fmt.Fprintf(w, "\x1b[31m%s\x1b[0m\n", line)
		case strings.HasPrefix(line, "@@"):
			fmt.Fprintf(w, "\x1b[36m%s\x1b[0m\n", line)
		default:
			fmt.Fprintln(w, line)
		}
	}
	return nil
}

// Confirm asks a yes/no question. It uses gum when available.
func Confirm(question string) bool {
	if !IsTerminal(os.Stdin) {
		return false
	}
	if gum, err := exec.LookPath("gum"); err == nil {
		cmd := exec.Command(gum, "confirm", "--default=false", question)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run() == nil
	}
	fmt.Printf("%s [y/N] ", question)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes"
}

// Ask reads one line of text, showing a default.
func Ask(question, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", question, def)
	} else {
		fmt.Printf("%s: ", question)
	}
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if line = strings.TrimSpace(line); line == "" {
		return def
	}
	return line
}

// AskSecret reads one line without showing it on screen.
func AskSecret(question string) (string, error) {
	fmt.Print(question + ": ")
	if IsTerminal(os.Stdin) {
		off := exec.Command("stty", "-echo")
		off.Stdin = os.Stdin
		if off.Run() == nil {
			defer func() {
				on := exec.Command("stty", "echo")
				on.Stdin = os.Stdin
				on.Run()
				fmt.Println()
			}()
		}
	}
	r := bufio.NewReaderSize(os.Stdin, 64*1024)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// PickVersion shows the versions of a file and returns the number the
// user chose (1 = newest), or 0 if they left without choosing. With fzf
// it shows a live preview of what each version changed; without it, a
// numbered list.
func PickVersion(self, file string, versions []history.Version, now time.Time) int {
	lines := make([]string, len(versions))
	for i, v := range versions {
		lines[i] = VersionLine(i+1, v, now)
	}
	if fzf, err := exec.LookPath("fzf"); err == nil && os.Getenv("LOREFS_PLAIN") == "" {
		cmd := exec.Command(fzf,
			"--ansi", "--no-sort", "--no-multi", "--layout=reverse", "--height=100%",
			"--header", "Versions of "+file+"\nEnter: restore this version   Esc: leave without changing anything",
			"--preview", shellQuote(self)+" history-preview "+shellQuote(file)+" {1}",
			// Beside the list on a wide terminal, below it on a narrow one
			// (such as the floating window the file manager opens), so the
			// list is never cut off.
			"--preview-window", "right,55%,wrap,<150(down,65%,wrap)")
		cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			return 0
		}
		var n int
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
		return n
	}
	fmt.Printf("Versions of %s (newest first)\n\n", file)
	fmt.Printf("%3s  %-34s %-14s %10s\n", "#", "saved", "by", "size")
	for _, l := range lines {
		fmt.Println(l)
	}
	fmt.Println()
	ans := Ask("Number of the version to restore (Enter to leave without changing anything)", "")
	var n int
	fmt.Sscanf(ans, "%d", &n)
	if n < 1 || n > len(versions) {
		return 0
	}
	return n
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
