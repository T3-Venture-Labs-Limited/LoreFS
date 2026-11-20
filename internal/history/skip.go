package history

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// MetaDir is the hidden folder at the top of the drive that holds
// history, journals and sandboxes.
const MetaDir = ".lorefs"

// DefaultSkipDirs are folder names whose contents are ignored entirely.
var DefaultSkipDirs = []string{
	MetaDir, ".trash", ".Trash-*", ".git", "node_modules", "__pycache__", ".venv",
}

// DefaultSkipFiles are file-name patterns that are ignored entirely:
// editor temporary files, lock files and partial downloads. They get no
// versions, are not logged and are not kept cached.
var DefaultSkipFiles = []string{
	"*.tmp", "*.temp", "*.swp", "*.swx", "*.swo", "*~", ".#*", "#*#",
	".~lock.*", "*.lck", "*.part", "*.partial", "*.crdownload",
	".goutputstream-*", "4913", ".DS_Store", ".lorefs-*",
	// JuiceFS's own control files at the top of the drive.
	".control", ".config", ".stats", ".accesslog",
}

// DefaultSkipMedia are video files. They are real content, so they are
// logged and kept cached like any other file, but they get no versions:
// they are large, and they get replaced rather than edited, so keeping
// every replaced copy for 30 days would cost storage for little benefit.
// A deleted or replaced video can still be brought back from the drive's
// trash for 7 days (`lorefs trash`).
var DefaultSkipMedia = []string{
	"*.mp4", "*.mov", "*.mkv", "*.webm", "*.avi", "*.m4v",
}

// Skip decides what is left out of version history.
//
// There are two degrees. Dirs and Files are ignored entirely. NoVersion
// files are ordinary content that simply gets no versions.
type Skip struct {
	Dirs      []string
	Files     []string
	NoVersion []string
}

// DefaultSkip returns the built-in skip list.
func DefaultSkip() *Skip {
	return &Skip{
		Dirs:      append([]string(nil), DefaultSkipDirs...),
		Files:     append([]string(nil), DefaultSkipFiles...),
		NoVersion: append([]string(nil), DefaultSkipMedia...),
	}
}

// LoadSkip returns the built-in list adjusted by a file of extra rules.
// Each line is a pattern. A trailing "/" makes it a folder to ignore; any
// other pattern names files that get no versions. A leading "!" removes a
// built-in pattern. A missing file means the defaults.
func LoadSkip(path string) (*Skip, error) {
	s := DefaultSkip()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		remove := strings.HasPrefix(line, "!")
		line = strings.TrimPrefix(line, "!")
		isDir := strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		switch {
		case remove && isDir:
			s.Dirs = without(s.Dirs, line)
		case remove:
			s.Files = without(s.Files, line)
			s.NoVersion = without(s.NoVersion, line)
		case isDir:
			if !contains(s.Dirs, line) {
				s.Dirs = append(s.Dirs, line)
			}
		default:
			if !contains(s.NoVersion, line) && !contains(s.Files, line) {
				s.NoVersion = append(s.NoVersion, line)
			}
		}
	}
	// The drive's own bookkeeping can never be recorded.
	for _, must := range []string{MetaDir, ".trash"} {
		if !contains(s.Dirs, must) {
			s.Dirs = append(s.Dirs, must)
		}
	}
	for _, must := range []string{".lorefs-*", ".control", ".config", ".stats", ".accesslog"} {
		if !contains(s.Files, must) {
			s.Files = append(s.Files, must)
		}
	}
	return s, sc.Err()
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func without(list []string, v string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}

// matchAnyFold is matchAny without regard to upper and lower case, so
// that CLIP.MP4 from a camera is treated like clip.mp4.
func matchAnyFold(patterns []string, name string) bool {
	name = strings.ToLower(name)
	for _, p := range patterns {
		if ok, _ := filepath.Match(strings.ToLower(p), name); ok {
			return true
		}
	}
	return false
}

// Dir reports whether a folder with this name is ignored.
func (s *Skip) Dir(name string) bool { return matchAny(s.Dirs, name) }

// File reports whether a file with this name is ignored entirely.
func (s *Skip) File(name string) bool { return matchAnyFold(s.Files, name) }

// Unversioned reports whether a file with this name is ordinary content
// that gets no versions.
func (s *Skip) Unversioned(name string) bool {
	return !s.File(name) && matchAnyFold(s.NoVersion, name)
}

// Media reports whether a file gets no versions because it is a video.
func (s *Skip) Media(name string) bool {
	return s.Unversioned(name) && matchAnyFold(DefaultSkipMedia, name)
}

// Ignored reports whether a path relative to the drive is ignored
// entirely, because one of its folders or its file name matches.
func (s *Skip) Ignored(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, p := range parts {
		if i < len(parts)-1 {
			if s.Dir(p) {
				return true
			}
		} else if s.File(p) || s.Dir(p) {
			return true
		}
	}
	return false
}

// Path reports whether a path relative to the drive gets no versions,
// either because it is ignored or because its type is not versioned.
func (s *Skip) Path(rel string) bool {
	return s.Ignored(rel) || s.Unversioned(filepath.Base(rel))
}
