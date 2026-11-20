package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/create"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/setup"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/token"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/ui"
)

func cmdJoin(args []string) error {
	fs := flags("join")
	name := fs.String("name", "", "short name for this computer, shown in file history")
	folder := fs.String("folder", "", "where the drive should appear (default ~/Lore)")
	tokenFile := fs.String("token-file", "", "read the join code from this file")
	tokenStdin := fs.Bool("token-stdin", false, "read the join code from standard input")
	fs.Bool("yes", false, "accepted for older scripts; joining asks nothing")
	cacheDir := fs.String("cache-dir", "", "where to keep the local cache (default ~/.cache/lorefs)")
	cacheLimit := fs.Int("cache-limit-mb", 0, "largest size of the local cache in MB (default 51200)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if config.Joined() {
		cfg, _ := config.Load()
		fmt.Printf("This computer has already joined the drive %q at %s. Nothing was changed.\n", cfg.DriveName, cfg.Folder)
		fmt.Println("To connect to a different drive, run `lorefs leave` first.")
		return nil
	}
	if err := setup.CheckSession(); err != nil {
		fmt.Fprintf(os.Stderr, "%v.\n\n%s\n", err, setup.NoSessionAdvice)
		return exitCode(1)
	}

	var code string
	switch {
	case *tokenFile != "":
		data, err := os.ReadFile(*tokenFile)
		if err != nil {
			return err
		}
		code = string(data)
	case *tokenStdin || !ui.IsTerminal(os.Stdin):
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			return err
		}
		code = string(data)
	default:
		fmt.Println("Paste the join code you were given. It will not be shown on screen.")
		var err error
		if code, err = ui.AskSecret("Join code"); err != nil {
			return err
		}
	}
	// Check the code's shape before asking anything else.
	if _, err := token.Decode(code); err != nil {
		return notValid(err)
	}

	machine := *name
	if machine == "" {
		host, _ := os.Hostname()
		if ui.IsTerminal(os.Stdin) && *tokenFile == "" && !*tokenStdin {
			fmt.Println("Choose a short name for this computer. It is shown beside the changes you make.")
			machine = ui.Ask("Name", host)
		} else {
			machine = host
		}
	}

	cfg, err := setup.Join(setup.JoinOptions{
		Token: code, Machine: machine, Folder: *folder, CacheDir: *cacheDir, CacheLimitMB: *cacheLimit,
		Progress: func(s string) { fmt.Println(s) },
	})
	switch {
	case errors.Is(err, token.ErrInvalid), errors.Is(err, setup.ErrTokenRejected):
		return notValid(err)
	case err != nil:
		return err
	}
	fmt.Printf("\nDone. The shared drive %q is at %s.\n", cfg.DriveName, cfg.Folder)
	fmt.Println("It will connect by itself every time you log in. Run `lorefs status` at any time to check it.")
	return nil
}

func notValid(err error) error {
	return fmt.Errorf("that join code is not valid (%v). Nothing on this computer was changed.\nAsk for a new code from `lorefs invite` and try again", unwrapMessage(err))
}

func unwrapMessage(err error) string {
	msg := err.Error()
	for _, prefix := range []string{token.ErrInvalid.Error() + ": ", setup.ErrTokenRejected.Error() + ": "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
}

func cmdInvite(args []string) error {
	fs := flags("invite")
	quiet := fs.Bool("quiet", false, "print only the code")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	code, err := setup.Invite()
	if errors.Is(err, token.ErrTrialDrive) {
		fmt.Println("This is a trial drive: it lives on this computer only, so there is no join code.")
		fmt.Println("To share files with other computers, create a shared drive with `lorefs create`")
		fmt.Println("(see the documentation for what you need: a bucket and an index).")
		return exitCode(1)
	}
	if err != nil {
		return err
	}
	if *quiet {
		fmt.Println(code)
		return nil
	}
	fmt.Fprintln(os.Stderr, "Join code for another computer (one line):")
	fmt.Fprintln(os.Stderr)
	fmt.Println(code)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Treat this like a password. Anyone who has it can read, change and delete")
	fmt.Fprintln(os.Stderr, "every file on the drive. Send it through a private channel, not by email")
	fmt.Fprintln(os.Stderr, "or a public chat. On the other computer: run the installer, then `lorefs join`.")
	return nil
}

func cmdLeave(args []string) error {
	fs := flags("leave")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	deleteCache := fs.Bool("delete-cache", false, "also delete the local cache")
	deleteDrive := fs.Bool("delete-drive", false, "also delete the drive itself (trial drives only)")
	keepCache := fs.Bool("keep-cache", false, "keep the local cache without asking")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *deleteDrive && !cfg.IsLocal() {
		return errors.New("--delete-drive only works on a trial drive. A shared drive belongs to everyone who uses it; " +
			"delete its bucket and index yourself if you are sure")
	}
	if *deleteDrive && !*yes {
		fmt.Printf("This DELETES the trial drive %q and every file in it, for good.\n", cfg.DriveName)
		if !ui.Confirm("Delete the trial drive?") {
			fmt.Println("Nothing was changed.")
			return nil
		}
		*yes = true
	}
	if !*yes {
		fmt.Printf("This disconnects this computer from the shared drive %q.\n", cfg.DriveName)
		fmt.Println("The drive and its files stay as they are for everyone else.")
		if !ui.Confirm("Disconnect this computer?") {
			fmt.Println("Nothing was changed.")
			return nil
		}
	}
	drop := *deleteCache
	if !drop && !*keepCache && !*yes {
		drop = ui.Confirm(fmt.Sprintf("Also delete the local cache in %s to free disk space?", cfg.CacheDir))
	}
	if err := setup.Leave(drop || *deleteDrive); err != nil {
		return err
	}
	if *deleteDrive {
		if err := create.RemoveLocal(cfg); err != nil {
			return err
		}
		fmt.Println("The trial drive was deleted.")
		return nil
	}
	fmt.Println("This computer is disconnected from the drive. To connect again you need a new join code.")
	return nil
}

func cmdContainers(args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return errUsage("use: lorefs containers on   or   lorefs containers off")
	}
	on := args[0] == "on"
	err := setup.SetContainers(on)
	if errors.Is(err, setup.ErrNeedsAdminStep) {
		fmt.Println("Containers and other users can only see the drive after one change that needs")
		fmt.Println("administrator rights. Run this once, then run `lorefs containers on` again:")
		fmt.Println()
		fmt.Println("  " + setup.AdminStep)
		return exitCode(1)
	}
	if err != nil {
		return err
	}
	if on {
		cfg, _ := config.Load()
		fmt.Println("Container access is on. The drive reconnected with it enabled.")
		fmt.Printf("Give a container the drive with:  -v %s:%s\n", cfg.Folder, cfg.Folder)
	} else {
		fmt.Println("Container access is off. Only you can see the drive on this computer.")
	}
	return nil
}
