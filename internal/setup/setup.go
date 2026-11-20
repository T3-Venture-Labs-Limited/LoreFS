// Package setup gets the drive onto a machine and off it again: writing
// the service files, joining with a token, issuing a token, leaving, and
// switching container access.
package setup

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/assets"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/mount"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/peers"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/token"
)

const (
	mountUnit  = "lorefs-mount.service"
	helperUnit = "lorefs-helper.service"
)

// Errors with plain-language text.
var (
	ErrAlreadyJoined = errors.New("this computer has already joined a drive")
	ErrTokenRejected = errors.New("the join code did not work")
)

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func unitDir() string { return filepath.Join(home(), ".config", "systemd", "user") }

func extensionFile() string {
	return filepath.Join(home(), ".local", "share", "nautilus-python", "extensions", "lorefs_history.py")
}

func noSystemd() bool { return os.Getenv("LOREFS_NO_SYSTEMD") != "" }

// ErrNoSession is returned when this computer has no systemd user session
// to keep the drive connected with.
var ErrNoSession = errors.New("this computer has no systemd user session")

// CheckSession makes sure the drive can be kept connected: LoreFS runs its
// mount and helper as systemd user services. It changes nothing.
func CheckSession() error {
	if noSystemd() {
		return nil
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("%w (the program systemctl was not found)", ErrNoSession)
	}
	if out, err := exec.Command("systemctl", "--user", "show-environment").CombinedOutput(); err != nil {
		return fmt.Errorf("%w (%s)", ErrNoSession, strings.TrimSpace(string(out)))
	}
	return nil
}

// NoSessionAdvice explains what to do about ErrNoSession.
const NoSessionAdvice = `LoreFS keeps the drive connected with two systemd user services, so it needs a
computer where you are logged in to a systemd user session (a normal Linux
desktop, or a server where you logged in with a login shell). Nothing was
changed on this computer.

In a container or a minimal environment there is no such session. You can
still use LoreFS's commands that do not need it, or run the mount by hand:
  lorefs mount run     keeps the drive connected while it runs
  lorefs helper run    records versions while it runs`

func systemctl(args ...string) error {
	if noSystemd() {
		return nil
	}
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// writeIfChanged writes a file and reports whether its content changed.
func writeIfChanged(path, content string, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// InstallFiles writes the service files and the file-manager extension.
// It reports whether a service file changed, so the caller knows whether
// running services need a restart.
func InstallFiles() (unitsChanged bool, err error) {
	if noSystemd() {
		return false, nil
	}
	a, err := writeIfChanged(filepath.Join(unitDir(), mountUnit), assets.MountUnit, 0o644)
	if err != nil {
		return false, err
	}
	b, err := writeIfChanged(filepath.Join(unitDir(), helperUnit), assets.HelperUnit, 0o644)
	if err != nil {
		return false, err
	}
	// The file-manager entry only where the file manager can use it.
	if found(DetectOptional(RealProbe()), PieceFileManager) {
		if _, err := writeIfChanged(extensionFile(), assets.NautilusExtension, 0o644); err != nil {
			return false, err
		}
	}
	if a || b {
		if err := systemctl("daemon-reload"); err != nil {
			return true, err
		}
	}
	return a || b, nil
}

// JoinOptions are the answers `lorefs join` collects.
type JoinOptions struct {
	Token   string
	Machine string
	Folder  string // empty = ~/Lore
	// CacheDir and CacheLimitMB override the defaults when set.
	CacheDir     string
	CacheLimitMB int
	Progress     func(string)
}

// Join connects this machine to a drive. If anything fails before the
// settings are written, nothing on the machine is changed.
func Join(opt JoinOptions) (config.Config, error) {
	say := opt.Progress
	if say == nil {
		say = func(string) {}
	}
	cfg := config.Defaults()
	if config.Joined() {
		return cfg, ErrAlreadyJoined
	}
	tok, err := token.Decode(opt.Token)
	if err != nil {
		return cfg, err
	}
	say("Checking the join code with the drive...")
	target := index.Target{Host: tok.Host, Port: tok.Port, DB: tok.DB, TLS: tok.TLS, Password: tok.Password, CAPEM: []byte(tok.CA)}
	conn, err := index.Dial(target, 15*time.Second)
	if err != nil {
		return cfg, fmt.Errorf("%w: %v", ErrTokenRejected, err)
	}
	_, formatted := conn.IsFormatted()
	conn.Close()
	if !formatted {
		return cfg, fmt.Errorf("%w: it points at an index that holds no drive", ErrTokenRejected)
	}

	cfg.DriveName = tok.Drive
	cfg.Machine = history.CleanMachine(opt.Machine)
	if opt.Folder != "" {
		cfg.Folder = opt.Folder
	}
	cfg.IndexScheme, cfg.IndexHost, cfg.IndexPort, cfg.IndexDB, cfg.IndexTLS = tok.Scheme, tok.Host, tok.Port, tok.DB, tok.TLS
	// Whatever this computer's user numbers are, the drive's files act as
	// the user and group that made the drive (see mount.Args).
	cfg.OwnerUID, cfg.OwnerGID = tok.UID, tok.GID
	if tok.GID == 0 { // a code from the earlier format carries no group
		cfg.OwnerGID = os.Getgid()
	}
	if opt.CacheDir != "" {
		cfg.CacheDir = opt.CacheDir
	}
	if opt.CacheLimitMB > 0 {
		cfg.CacheLimitMB = opt.CacheLimitMB
	}
	if cfg.CacheDir, err = filepath.Abs(cfg.CacheDir); err != nil {
		return cfg, err
	}
	if cfg.Folder, err = filepath.Abs(cfg.Folder); err != nil {
		return cfg, err
	}
	if entries, err := os.ReadDir(cfg.Folder); err == nil && len(entries) > 0 && !jfs.IsMounted(cfg.Folder) {
		return cfg, fmt.Errorf("the folder %s already exists and is not empty; choose another with --folder", cfg.Folder)
	}

	return cfg, Connect(cfg, tok.Password, []byte(tok.CA), say)
}

// Connect saves the settings of a drive on this computer, installs the
// services and waits for the folder to appear. password and ca are empty
// for a drive that needs neither (a trial drive, or an index whose
// certificate comes from a public authority).
func Connect(cfg config.Config, password string, ca []byte, say func(string)) error {
	if say == nil {
		say = func(string) {}
	}
	if len(ca) > 0 {
		if err := config.WritePrivate(config.CAFile(), ca); err != nil {
			return err
		}
	}
	if password != "" {
		if err := config.SavePassword(password); err != nil {
			return err
		}
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Folder, 0o700); err != nil {
		return err
	}
	if !jfs.IsMounted(cfg.Folder) {
		os.Chmod(cfg.Folder, 0o500) // read-only until the drive is mounted on it
	}
	if noSystemd() {
		return nil
	}
	if _, err := InstallFiles(); err != nil {
		return err
	}
	pieces := DetectOptional(RealProbe())
	if found(pieces, PieceBookmarks) {
		if err := addBookmark(cfg.Folder); err != nil {
			say("Could not add the file-manager sidebar entry: " + err.Error())
		}
	}
	if r := Report(pieces); r != "" {
		say(strings.TrimRight(r, "\n"))
	}
	say("Connecting the drive...")
	if err := systemctl("enable", "--now", mountUnit, helperUnit); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Minute)
	for !jfs.IsMounted(cfg.Folder) {
		if time.Now().After(deadline) {
			return errors.New("the drive did not connect within 2 minutes; run `lorefs status` to see why")
		}
		time.Sleep(time.Second)
	}
	return nil
}

// Invite builds a join token from this machine's settings.
func Invite() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	target, err := mount.Target(cfg)
	if err != nil {
		return "", err
	}
	return token.Encode(token.Token{
		Drive: cfg.DriveName, Scheme: cfg.IndexScheme, Host: cfg.IndexHost, Port: cfg.IndexPort, DB: cfg.IndexDB,
		TLS: cfg.IndexTLS, Password: target.Password, CA: string(target.CAPEM), UID: cfg.OwnerUID, GID: cfg.OwnerGID,
	})
}

// Leave disconnects this machine. Nothing on the drive is changed,
// apart from the note saying which version of lorefs this machine runs.
func Leave(deleteCache bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if jfs.IsMounted(cfg.Folder) {
		peers.Remove(cfg.Folder, cfg.Machine)
	}
	if !noSystemd() {
		// Stopping may fail if a program still has a file open; go on anyway.
		systemctl("disable", "--now", helperUnit)
		if err := systemctl("disable", "--now", mountUnit); err != nil {
			return fmt.Errorf("the drive is still in use; close programs using %s and try again (%v)", cfg.Folder, err)
		}
		os.Remove(filepath.Join(unitDir(), mountUnit))
		os.Remove(filepath.Join(unitDir(), helperUnit))
		os.Remove(extensionFile())
		systemctl("daemon-reload")
		removeBookmark(cfg.Folder)
	} else if jfs.IsMounted(cfg.Folder) {
		if err := mount.Unmount(cfg); err != nil {
			return err
		}
	}
	if jfs.IsMounted(cfg.Folder) {
		return fmt.Errorf("the drive is still mounted at %s; close programs using it and try again", cfg.Folder)
	}
	os.Chmod(cfg.Folder, 0o700)
	os.Remove(cfg.Folder) // only succeeds when empty, which it is once unmounted
	for _, f := range []string{config.File(), config.SecretFile(), config.CAFile(), config.PinsFile()} {
		os.Remove(f)
	}
	os.RemoveAll(config.StateDir())
	if deleteCache {
		os.RemoveAll(cfg.CacheDir)
	}
	return nil
}

// --- file-manager bookmark ---

func bookmarksFile() string { return filepath.Join(home(), ".config", "gtk-3.0", "bookmarks") }

func bookmarkLine(folder string) string {
	u := url.URL{Scheme: "file", Path: folder}
	return u.String() + " " + filepath.Base(folder)
}

func addBookmark(folder string) error {
	line := bookmarkLine(folder)
	data, _ := os.ReadFile(bookmarksFile())
	prefix := strings.SplitN(line, " ", 2)[0]
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		if strings.SplitN(sc.Text(), " ", 2)[0] == prefix {
			return nil
		}
	}
	body := string(data)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.MkdirAll(filepath.Dir(bookmarksFile()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(bookmarksFile(), []byte(body+line+"\n"), 0o644)
}

func removeBookmark(folder string) {
	data, err := os.ReadFile(bookmarksFile())
	if err != nil {
		return
	}
	prefix := strings.SplitN(bookmarkLine(folder), " ", 2)[0]
	var keep []string
	for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.SplitN(l, " ", 2)[0] != prefix {
			keep = append(keep, l)
		}
	}
	body := strings.Join(keep, "\n")
	if body != "" {
		body += "\n"
	}
	os.WriteFile(bookmarksFile(), []byte(body), 0o644)
}

// --- container access ---

// FuseAllowsOthers reports whether /etc/fuse.conf lets a user's mount be
// visible to other users and to containers.
func FuseAllowsOthers() bool {
	data, err := os.ReadFile("/etc/fuse.conf")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == "user_allow_other" {
			return true
		}
	}
	return false
}

// AdminStep is the one command an administrator must run before
// container access can be turned on.
const AdminStep = "echo user_allow_other | sudo tee -a /etc/fuse.conf"

// ErrNeedsAdminStep is returned when that command has not been run yet.
var ErrNeedsAdminStep = errors.New("an administrator step is needed first")

// SetContainers turns container access on or off and remounts the drive.
func SetContainers(on bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if on && !FuseAllowsOthers() {
		return ErrNeedsAdminStep
	}
	if cfg.Containers == on {
		return nil
	}
	cfg.Containers = on
	if err := cfg.Save(); err != nil {
		return err
	}
	return systemctl("restart", mountUnit)
}

// RestartIfRunning restarts the services after an upgrade changed them.
func RestartIfRunning() error {
	if noSystemd() || !config.Joined() {
		return nil
	}
	if exec.Command("systemctl", "--user", "is-active", "--quiet", mountUnit).Run() != nil {
		return nil
	}
	return systemctl("restart", mountUnit)
}

// RestartHelper restarts only the helper, which is safe at any time.
func RestartHelper() error {
	if noSystemd() || !config.Joined() {
		return nil
	}
	if exec.Command("systemctl", "--user", "is-enabled", "--quiet", helperUnit).Run() != nil {
		return nil
	}
	return systemctl("restart", helperUnit)
}
