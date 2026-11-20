package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Piece is something optional that makes LoreFS nicer on a desktop. A
// computer without it works, just without that nicety.
type Piece struct {
	ID    string
	Name  string
	Gives string // what it adds, in plain words
	Found bool
	Hint  string // how to get it
}

// Probe is how pieces are looked for; tests replace it.
type Probe struct {
	LookPath func(name string) (string, error)
	Glob     func(pattern string) ([]string, error)
	Exists   func(path string) bool
	Home     string
}

// RealProbe looks at this computer.
func RealProbe() Probe {
	home, _ := os.UserHomeDir()
	return Probe{
		LookPath: exec.LookPath,
		Glob:     filepath.Glob,
		Exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
		Home:     home,
	}
}

// Piece identifiers.
const (
	PieceFileManager = "file-manager"
	PieceBookmarks   = "bookmarks"
	PieceNotify      = "notifications"
	PieceTerminal    = "terminal"
	PieceChooser     = "chooser"
)

// DetectOptional looks for the optional pieces.
func DetectOptional(p Probe) []Piece {
	has := func(name string) bool { _, err := p.LookPath(name); return err == nil }
	var out []Piece

	nautilusExt := false
	if has("nautilus") {
		for _, pat := range []string{"/usr/lib*/nautilus/extensions-*/libnautilus-python.so", "/usr/lib/*/nautilus/extensions-*/libnautilus-python.so"} {
			if m, _ := p.Glob(pat); len(m) > 0 {
				nautilusExt = true
			}
		}
	}
	out = append(out, Piece{ID: PieceFileManager, Name: "GNOME Files with Python extensions",
		Gives: "a \"Version history\" entry when you right-click a file", Found: nautilusExt,
		Hint: "install nautilus-python (python-nautilus on some distributions)"})

	gtk := p.Exists(filepath.Join(p.Home, ".config", "gtk-3.0")) || has("nautilus") || has("thunar") || has("nemo") || has("pcmanfm")
	out = append(out, Piece{ID: PieceBookmarks, Name: "a file manager that reads GTK bookmarks",
		Gives: "an entry for the drive in the file manager's sidebar", Found: gtk,
		Hint: "use GNOME Files, Thunar, Nemo or another GTK file manager"})

	out = append(out, Piece{ID: PieceNotify, Name: "notify-send",
		Gives: "a desktop notice when the drive stops or comes back", Found: has("notify-send"),
		Hint: "install libnotify (libnotify-bin on Debian and Ubuntu)"})

	out = append(out, Piece{ID: PieceTerminal, Name: "xdg-terminal-exec",
		Gives: "opening the version-history view from the file manager's right-click entry", Found: has("xdg-terminal-exec"),
		Hint: "install xdg-terminal-exec"})

	out = append(out, Piece{ID: PieceChooser, Name: "fzf or gum",
		Gives: "choosing a version in a list instead of typing its number", Found: has("fzf") || has("gum"),
		Hint: "install fzf or gum"})
	return out
}

// Skipped returns the pieces that were not found.
func Skipped(pieces []Piece) []Piece {
	var out []Piece
	for _, p := range pieces {
		if !p.Found {
			out = append(out, p)
		}
	}
	return out
}

// Report lists what was skipped, one line each, or "" when everything
// optional is there.
func Report(pieces []Piece) string {
	var b strings.Builder
	for _, p := range Skipped(pieces) {
		fmt.Fprintf(&b, "  - %s was not found, so there is no %s (to get it: %s)\n", p.Name, p.Gives, p.Hint)
	}
	if b.Len() == 0 {
		return ""
	}
	return "Optional pieces that were not found and were skipped:\n" + b.String()
}

func found(pieces []Piece, id string) bool {
	for _, p := range pieces {
		if p.ID == id {
			return p.Found
		}
	}
	return false
}
