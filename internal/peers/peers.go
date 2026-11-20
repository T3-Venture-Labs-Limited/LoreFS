// Package peers lets each computer on a drive say which version of the
// program it runs, so that `lorefs status` can tell when they differ.
//
// Each computer's helper writes a small note in the drive's bookkeeping
// folder. Nothing depends on the notes: they only inform.
package peers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
)

// Note is what one computer says about itself.
type Note struct {
	Machine string `json:"machine"`
	Version string `json:"version"`
	// CommitTime is when the installed version was committed (seconds
	// since 1970), which puts two versions in order. 0 when unknown.
	CommitTime int64     `json:"commit_time"`
	JuiceFS    string    `json:"juicefs,omitempty"`
	Seen       time.Time `json:"seen"`
}

// Dir is where the notes are kept on the drive.
func Dir(root string) string { return filepath.Join(root, history.MetaDir, "machines") }

func file(root, machine string) string {
	return filepath.Join(Dir(root), history.CleanMachine(machine)+".json")
}

// Write saves this computer's note, replacing its previous one in one step.
func Write(root string, n Note) error {
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	dst := file(root, n.Machine)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Remove deletes a computer's note, when it leaves the drive.
func Remove(root, machine string) {
	os.Remove(file(root, machine))
}

// Read returns every computer's note, sorted by name. Notes that cannot
// be read are left out.
func Read(root string) []Note {
	entries, err := os.ReadDir(Dir(root))
	if err != nil {
		return nil
	}
	var out []Note
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(Dir(root), e.Name()))
		if err != nil {
			continue
		}
		var n Note
		if json.Unmarshal(data, &n) != nil || n.Machine == "" || n.Version == "" {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Machine < out[b].Machine })
	return out
}
