// Package assets holds the files that lorefs writes onto a machine or a
// new drive: the systemd units, the Nautilus extension and the rules
// file for agents.
package assets

import _ "embed"

//go:embed files/lorefs-mount.service
var MountUnit string

//go:embed files/lorefs-helper.service
var HelperUnit string

//go:embed files/lorefs_history.py
var NautilusExtension string

//go:embed files/AGENTS.md
var AgentsRules string
