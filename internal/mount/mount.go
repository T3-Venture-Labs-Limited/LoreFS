// Package mount turns this machine's settings into the JuiceFS mount
// command and handles the folder's permissions around mounting.
package mount

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
)

// IndexURL builds the address JuiceFS uses for the index. The password is
// never part of it; it travels in the META_PASSWORD environment variable.
func IndexURL(c config.Config) string {
	if c.IsLocal() {
		return "sqlite3://" + c.IndexPath
	}
	q := url.Values{}
	if c.IndexTLS {
		q.Set("tls-ca-cert-file", config.CAFile())
	}
	if c.ClientCache {
		q.Set("client-cache", "true")
	}
	// Give up on an unanswered request after 10 seconds instead of 30, so
	// an unreachable index produces an error rather than a long hang.
	q.Set("read-timeout", "10s")
	u := url.URL{
		Scheme:   c.IndexScheme,
		Host:     net.JoinHostPort(c.IndexHost, strconv.Itoa(c.IndexPort)),
		Path:     "/" + strconv.Itoa(c.IndexDB),
		RawQuery: q.Encode(),
	}
	return u.String()
}

// Args returns the arguments of `juicefs mount` for this machine.
// A save must only return once the data is stored remotely, so
// --writeback is never passed.
func Args(c config.Config) []string {
	args := []string{
		"mount", IndexURL(c), c.Folder,
		"--cache-dir", c.CacheDir,
		"--cache-size", strconv.Itoa(c.CacheLimitMB),
		"--free-space-ratio", "0.1",
		"--backup-meta", "1h",
		"--io-retries", "8",
		// Act as the drive's owner on every computer, whatever this
		// computer's user numbers are (drive-mount spec).
		"--all-squash", fmt.Sprintf("%d:%d", c.OwnerUID, c.OwnerGID),
		"--no-usage-report",
		"--no-syslog",
		// Keep JuiceFS's own files (.accesslog never ends when read) out of
		// folder listings, so a recursive search over the drive cannot hang.
		"--hide-internal",
		// Every path component costs one round trip to the index once the
		// kernel has forgotten it. Remember files briefly and folders a
		// little longer; see docs/measurements.md for the effect.
		"--attr-cache", strconv.Itoa(c.FileCacheSec),
		"--entry-cache", strconv.Itoa(c.FileCacheSec),
		"--dir-entry-cache", strconv.Itoa(c.FolderCacheSec),
		"--open-cache", strconv.Itoa(c.OpenCacheSec),
	}
	if c.Containers {
		args = append(args, "-o", "allow_other")
	}
	return args
}

// Target returns the index login for this machine.
func Target(c config.Config) (index.Target, error) {
	if c.IsLocal() {
		return index.Target{}, nil // a trial drive has no server to log in to
	}
	pw, err := config.Password()
	if err != nil {
		return index.Target{}, err
	}
	t := index.Target{Host: c.IndexHost, Port: c.IndexPort, DB: c.IndexDB, TLS: c.IndexTLS, Password: pw}
	if c.IndexTLS {
		if t.CAPEM, err = os.ReadFile(config.CAFile()); err != nil {
			return t, err
		}
	}
	return t, nil
}

// Pre makes the folder ready to be mounted on. JuiceFS needs write
// permission on the folder at mount time.
func Pre(c config.Config) error {
	if jfs.IsMounted(c.Folder) {
		if !jfs.IsDead(c.Folder) {
			return nil
		}
		// A previous mount that died leaves a dead entry behind; clear it.
		exec.Command("fusermount3", "-uz", c.Folder).Run()
	}
	if err := os.MkdirAll(c.Folder, 0o700); err != nil {
		return err
	}
	return os.Chmod(c.Folder, 0o700)
}

// Post runs after the mount has stopped for any reason. It removes a dead
// mount entry and makes the empty folder read-only, so that nothing can
// be written to the local disk by mistake while the drive is not mounted.
func Post(c config.Config) error {
	if jfs.IsMounted(c.Folder) {
		if !jfs.IsDead(c.Folder) {
			return nil // still mounted (for example a restart raced us)
		}
		exec.Command("fusermount3", "-uz", c.Folder).Run()
	}
	if err := os.MkdirAll(c.Folder, 0o700); err != nil {
		return err
	}
	return os.Chmod(c.Folder, 0o500)
}

// Run replaces this process with `juicefs mount` in the foreground.
func Run(c config.Config) error {
	var pw string
	if !c.IsLocal() {
		var err error
		if pw, err = config.Password(); err != nil {
			return err
		}
	}
	bin := jfs.Binary()
	path, err := exec.LookPath(bin)
	if err != nil {
		return fmt.Errorf("the juicefs program was not found; run the installer again")
	}
	if err := os.MkdirAll(c.CacheDir, 0o700); err != nil {
		return err
	}
	env := withoutEnv(os.Environ(), "META_PASSWORD")
	if pw != "" {
		env = append(env, "META_PASSWORD="+pw)
	}
	return syscall.Exec(path, append([]string{"juicefs"}, Args(c)...), env)
}

func withoutEnv(env []string, key string) []string {
	out := env[:0:0]
	for _, e := range env {
		if !strings.HasPrefix(e, key+"=") {
			out = append(out, e)
		}
	}
	return out
}

// Unmount asks JuiceFS to unmount the folder.
func Unmount(c config.Config) error {
	out, err := exec.Command(jfs.Binary(), "umount", c.Folder).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Stop disconnects the drive for the service manager. A clean unmount is
// tried first. If a program is still using the folder, the unmount is
// refused; the folder is then detached at once instead, so that program
// gets an error on its next access rather than freezing until the
// service manager gives up. (Found the hard way: an upgrade restarted the
// drive while the file manager was showing it, and the file manager
// froze for 27 seconds.)
func Stop(c config.Config) error {
	if !jfs.IsMounted(c.Folder) {
		return nil
	}
	if err := Unmount(c); err == nil {
		return nil
	}
	return exec.Command("fusermount3", "-uz", c.Folder).Run()
}
