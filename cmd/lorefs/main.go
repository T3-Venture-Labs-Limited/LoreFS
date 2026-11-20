// Command lorefs sets up and looks after a LoreFS shared drive on this
// computer: joining, status, version history, fast folders and sandboxes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/helper"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/mount"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/setup"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/status"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/update"
)

// Set when the program is built (see Makefile).
var (
	version    = "dev"
	commitTime = "0" // when that version was committed, seconds since 1970
)

func commitUnix() int64 {
	n, _ := strconv.ParseInt(commitTime, 10, 64)
	return n
}

const usage = `lorefs looks after the LoreFS shared drive on this computer.

Getting connected
  lorefs create               Make a new shared drive (or a trial one with --local)
  lorefs join                 Connect this computer to a drive, using a join code
  lorefs invite               Print a join code for another computer
  lorefs leave                Disconnect this computer (the drive itself is untouched)
  lorefs status               Show whether the drive is working, and what to do if not

Running your own index
  lorefs server init          Write a folder that runs the drive's index (Valkey) with Docker
  lorefs admin changelog on|off|status   The drive's change log, which version history follows

Backup and restore
  lorefs backup setup|run|schedule|status   Keep a second copy of the files at another provider
  lorefs restore-index        Rebuild the drive's index from a copy (--list, --drill, --from-backup)

Earlier versions of a file
  lorefs history <file>       See a file's earlier versions and restore one
  lorefs diff <file>          Show what changed since the previous version
  lorefs restore <file> <n>   Make version number <n> the current one (1 is the newest)
  lorefs open <file> <n>      Open version <n> without changing the current file
  lorefs log                  Recent saves, deletions and restores on all computers
  lorefs trash [name]         Files deleted or replaced in the last 7 days
  lorefs trash restore <name> Bring one of them back

Keeping folders fast
  lorefs pin <folder>         Keep everything in a folder ready on this computer
  lorefs unpin <folder>       Stop keeping it ready
  lorefs pins                 List pinned folders and how much of each is ready

For agents
  lorefs sandbox create <folder> [name]   Make a private copy of a folder to work in
  lorefs sandbox list | diff <name> | apply <name> | remove <name>
  lorefs agents-snippet       Text to add to your agent instructions
  lorefs rules diff           Compare the drive's rules file with this version's template
  lorefs containers on|off    Let containers on this computer use the drive

Other
  lorefs selftest             Time folder listing and file opening on this computer
  lorefs update               Get the newest version of lorefs (--check only looks)
  lorefs version              Show the installed versions

Videos (mp4, mov and similar) are not versioned; use lorefs trash if one is deleted.

Add --json to status, history, log, trash, pins and sandbox list for output that programs can read.
`

// errUsage marks a mistake in how a command was called.
type errUsage string

func (e errUsage) Error() string { return string(e) }

// exitCode lets a command choose its exit status without printing.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(0)
	}
	cmd, args := os.Args[1], os.Args[2:]
	run, ok := commands[cmd]
	if !ok {
		switch cmd {
		case "help", "-h", "--help":
			fmt.Print(usage)
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "lorefs: there is no command called %q.\nRun `lorefs help` to see what lorefs can do.\n", cmd)
		os.Exit(64)
	}
	err := run(args)
	var code exitCode
	var usageErr errUsage
	switch {
	case err == nil:
	case errors.As(err, &code):
		os.Exit(int(code))
	case errors.As(err, &usageErr):
		fmt.Fprintf(os.Stderr, "lorefs %s: %s\n", cmd, err)
		os.Exit(64)
	case errors.Is(err, config.ErrNotJoined):
		fmt.Fprintln(os.Stderr, "This computer has not joined a LoreFS drive yet. Run `lorefs join`, or `lorefs status` for help.")
		os.Exit(3)
	case errors.Is(err, jfs.ErrNotMounted):
		fmt.Fprintln(os.Stderr, "The shared drive is not connected right now, so this cannot be done. Run `lorefs status` to see why.")
		os.Exit(3)
	default:
		fmt.Fprintf(os.Stderr, "lorefs %s: %s\n", cmd, err)
		os.Exit(1)
	}
}

var commands map[string]func([]string) error

func init() {
	commands = map[string]func([]string) error{
		"create":          cmdCreate,
		"server":          cmdServer,
		"admin":           cmdAdmin,
		"rules":           cmdRules,
		"backup":          cmdBackup,
		"restore-index":   cmdRestoreIndex,
		"join":            cmdJoin,
		"invite":          cmdInvite,
		"leave":           cmdLeave,
		"status":          cmdStatus,
		"history":         cmdHistory,
		"history-preview": cmdHistoryPreview,
		"diff":            cmdDiff,
		"restore":         cmdRestore,
		"open":            cmdOpen,
		"log":             cmdLog,
		"trash":           cmdTrash,
		"pin":             cmdPin,
		"unpin":           cmdUnpin,
		"pins":            cmdPins,
		"sandbox":         cmdSandbox,
		"containers":      cmdContainers,
		"agents-snippet":  cmdAgentsSnippet,
		"selftest":        cmdSelftest,
		"helper":          cmdHelper,
		"mount":           cmdMount,
		"setup-files":     cmdSetupFiles,
		"prune":           cmdPrune,
		"make-token":      cmdMakeToken,
		"index-check":     cmdIndexCheck,
		"update":          cmdUpdate,
		"version":         cmdVersion,
	}
}

// flags returns a flag set that reports mistakes as usage errors.
func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// parse accepts flags before or after the plain arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var plain []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, errUsage(err.Error())
		}
		args = fs.Args()
		if len(args) > 0 {
			plain = append(plain, args[0])
			args = args[1:]
		}
	}
	return plain, nil
}

// openStore loads the settings and returns the history store of the
// mounted drive.
func openStore() (config.Config, *history.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, nil, err
	}
	if !jfs.IsMounted(cfg.Folder) {
		return cfg, nil, jfs.ErrNotMounted
	}
	store := history.New(cfg.Folder, cfg.Machine, jfs.NewExec())
	if skip, err := history.LoadSkip(config.SkipFile()); err == nil {
		store.Skip = skip
	}
	if cfg.RetentionDays > 0 {
		store.Retention = time.Duration(cfg.RetentionDays) * 24 * time.Hour
	}
	return cfg, store, nil
}

// relInDrive turns what the user typed into a path relative to the drive.
// A relative path is tried against the current folder first, then
// against the top of the drive.
func relInDrive(store *history.Store, arg string) (string, error) {
	if rel, err := store.Rel(arg); err == nil {
		return rel, nil
	}
	if !filepath.IsAbs(arg) {
		if rel, err := store.Rel(filepath.Join(store.Root, arg)); err == nil {
			return rel, nil
		}
	}
	return "", fmt.Errorf("%s is not on the shared drive (%s)", arg, store.Root)
}

func cmdVersion([]string) error {
	when := ""
	if t := commitUnix(); t > 0 {
		when = " (" + time.Unix(t, 0).Local().Format("2 Jan 2006") + ")"
	}
	fmt.Printf("lorefs %s%s\nJuiceFS %s\n", version, when, jfs.NewExec().Version())
	return nil
}

func cmdUpdate(args []string) error {
	fs := flags("update")
	check := fs.Bool("check", false, "only say whether a newer version exists")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if !*check {
		fmt.Println("Looking for a newer release...")
	}
	res, err := update.Run(context.Background(), update.Options{Installed: version, CheckOnly: *check, Out: os.Stdout})
	if err != nil {
		return err
	}
	switch {
	case res.Newer && *check:
		fmt.Printf("A newer version is available: lorefs %s. This computer runs lorefs %s.\nInstall it with: lorefs update\n", res.Latest, version)
		return exitCode(1)
	case !res.Newer:
		fmt.Printf("lorefs %s is the newest version.\n", version)
	default:
		fmt.Printf("\nUpdated lorefs from %s to %s.\n", version, res.Latest)
		fmt.Println("Version history and status use the new version from now on. The drive")
		fmt.Println("itself switches at your next login; to switch now, close programs that")
		fmt.Println("are using the drive and run: systemctl --user restart lorefs-mount")
	}
	return nil
}

func cmdStatus(args []string) error {
	fs := flags("status")
	asJSON := fs.Bool("json", false, "output for programs")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	r := status.Collect(version, commitUnix())
	if *asJSON {
		if err := printJSON(r); err != nil {
			return err
		}
	} else {
		fmt.Print(r.Text())
	}
	if !r.Joined {
		return exitCode(3)
	}
	if code := r.ExitCode(); code != 0 {
		return exitCode(code)
	}
	return nil
}

func cmdHelper(args []string) error {
	if len(args) != 1 || args[0] != "run" {
		return errUsage("use `lorefs helper run` (the lorefs-helper service does this for you)")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	helper.Program.Version, helper.Program.CommitTime = version, commitUnix()
	return helper.Run(ctx, cfg)
}

func cmdMount(args []string) error {
	if len(args) != 1 {
		return errUsage("use `lorefs mount pre|run|stop|post` (the lorefs-mount service does this for you)")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	switch args[0] {
	case "pre":
		return mount.Pre(cfg)
	case "run":
		return mount.Run(cfg)
	case "stop":
		return mount.Stop(cfg)
	case "post":
		return mount.Post(cfg)
	}
	return errUsage("use `lorefs mount pre|run|stop|post`")
}

func cmdSetupFiles(args []string) error {
	changed, err := setup.InstallFiles()
	if err != nil {
		return err
	}
	if r := setup.Report(setup.DetectOptional(setup.RealProbe())); r != "" {
		fmt.Fprint(os.Stderr, r)
	}
	if changed {
		fmt.Println("service files updated")
		if len(args) == 1 && args[0] == "--restart" {
			return setup.RestartIfRunning()
		}
	}
	return nil
}

func cmdPrune([]string) error {
	_, store, err := openStore()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	res, ran, err := helper.PruneLocked(ctx, store)
	if err != nil {
		return err
	}
	if !ran {
		fmt.Println("Another computer is removing old versions right now; nothing was done here.")
		return nil
	}
	fmt.Printf("Removed %d old version(s) and %d old journal file(s).\n", res.Versions, res.Journals)
	return nil
}

func cmdAgentsSnippet([]string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	fmt.Print(strings.ReplaceAll(agentsSnippet, "{FOLDER}", cfg.Folder))
	return nil
}

const agentsSnippet = `## LoreFS shared drive

The folder {FOLDER} is LoreFS, a drive shared with colleagues and their
agents. Files saved there appear on every person's computer within
seconds. Use it for notes, documents, assets and handoffs that others
should see; do not keep code repositories, dependency folders or secrets
there.

Before writing to it, read {FOLDER}/AGENTS.md for the rules. Useful
commands: ` + "`lorefs status`" + ` (is the drive working), ` + "`lorefs history <file>`" + `
and ` + "`lorefs restore <file> <n>`" + ` (earlier versions), ` + "`lorefs log`" + ` (recent
activity), ` + "`lorefs sandbox create <folder> <name>`" + ` (a private copy for risky
changes).
`
