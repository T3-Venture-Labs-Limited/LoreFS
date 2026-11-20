// Package assets holds the files that lorefs writes onto a machine or a
// new drive: the systemd units, the Nautilus extension and the rules
// file for agents.
package assets

import (
	_ "embed"
	"strconv"
	"strings"
)

//go:embed files/lorefs-mount.service
var MountUnit string

//go:embed files/lorefs-helper.service
var HelperUnit string

//go:embed files/lorefs_history.py
var NautilusExtension string

//go:embed files/AGENTS.md
var AgentsRules string

// RulesVersion is the version of the rules file template. It is written at
// the end of the file, as a comment, so that a later program can tell whether
// the file on a drive came from an older template.
const RulesVersion = 1

// RulesVersionOf reads the template version from the end of a rules file.
// A file without one (edited by hand, or from before versions) is 0.
func RulesVersionOf(content string) int {
	const marker = "<!-- lorefs-rules-version: "
	i := strings.LastIndex(content, marker)
	if i < 0 {
		return 0
	}
	rest := content[i+len(marker):]
	j := strings.Index(rest, " -->")
	if j < 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest[:j]))
	if err != nil {
		return 0
	}
	return n
}
