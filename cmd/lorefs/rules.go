package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/assets"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
)

// cmdRules compares the drive's rules file for agents with the template
// this version of lorefs carries. It never changes the drive's file.
func cmdRules(args []string) error {
	if len(args) != 1 || args[0] != "diff" {
		return errUsage("use: lorefs rules diff")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	driveFile := filepath.Join(cfg.Folder, "AGENTS.md")
	data, err := os.ReadFile(driveFile)
	if err != nil {
		return fmt.Errorf("the drive has no rules file at %s (the helper writes one when it starts)", driveFile)
	}
	if string(data) == assets.AgentsRules {
		fmt.Println("The rules file on the drive is the same as this version's template.")
		return nil
	}
	tmp, err := os.CreateTemp("", "lorefs-rules-template-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	tmp.WriteString(assets.AgentsRules)
	tmp.Close()
	fmt.Printf("The drive's file is version %d; this program's template is version %d. Lines starting with - are only in the drive's file, + only in the template.\n\n",
		assets.RulesVersionOf(string(data)), assets.RulesVersion)
	cmd := exec.Command("diff", "-u", "--label", "the drive's AGENTS.md", "--label", "this version's template", driveFile, tmp.Name())
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil // differences found: that is the output
		}
		return err
	}
	return nil
}
