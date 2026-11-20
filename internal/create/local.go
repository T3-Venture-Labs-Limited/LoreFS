package create

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
)

// A drive's name must satisfy JuiceFS: letters, digits and hyphens, 3 to 63.
var validName = regexp.MustCompile(`^[A-Za-z0-9-]{3,63}$`)

// CheckName says whether a name can be used for a drive.
func CheckName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("the name %q is not usable: use 3 to 63 letters, digits and hyphens", name)
	}
	return nil
}

// LocalOptions say how to make a trial drive.
type LocalOptions struct {
	Name     string // default "trial"
	Progress func(string)
	Juicefs  Runner
}

// LocalRoot is the folder that holds every trial drive.
func LocalRoot() string { return filepath.Join(config.DataDir(), "local") }

// CreateLocal makes a drive that lives on this computer only: its index is
// an SQLite file and its files are kept in a folder, both under
// LocalRoot()/<name>. No account, network or password is involved.
func CreateLocal(ctx context.Context, o LocalOptions) (config.Config, error) {
	cfg := config.Defaults()
	if o.Name == "" {
		o.Name = "trial"
	}
	if err := CheckName(o.Name); err != nil {
		return cfg, err
	}
	say := o.Progress
	if say == nil {
		say = func(string) {}
	}
	dir := filepath.Join(LocalRoot(), o.Name)
	if _, err := os.Stat(dir); err == nil {
		return cfg, fmt.Errorf("a trial drive named %q already exists in %s. Choose another name, or delete it with `lorefs leave --delete-drive` if it is connected here", o.Name, dir)
	}
	runner := o.Juicefs
	if runner == nil {
		runner = ExecRunner{Bin: jfs.Binary()}
	}
	store := filepath.Join(dir, "files") + string(filepath.Separator)
	if err := os.MkdirAll(store, 0o700); err != nil {
		return cfg, err
	}
	fail := func(err error) (config.Config, error) {
		os.RemoveAll(dir) // leave nothing behind
		return cfg, err
	}
	cfg.DriveName = o.Name
	cfg.IndexScheme, cfg.IndexPath, cfg.IndexTLS = config.SchemeSQLite, filepath.Join(dir, "meta.db"), false
	meta := cfg.IndexURL()
	say("Creating a trial drive on this computer...")
	if out, err := runner.Run(ctx, nil, "format", "--storage", "file", "--bucket", store,
		"--trash-days", strconv.Itoa(TrashDays), meta, o.Name); err != nil {
		return fail(fmt.Errorf("the trial drive could not be created: %s", lastLine(out)))
	}
	if out, err := runner.Run(ctx, nil, "config", meta, "--changelog",
		"--changelog-max-age", ChangeLogAge, "--changelog-max-lines", strconv.Itoa(ChangeLogEntries)); err != nil {
		return fail(fmt.Errorf("the change log could not be turned on: %s", lastLine(out)))
	}
	cfg.OwnerUID, cfg.OwnerGID = os.Getuid(), os.Getgid()
	return cfg, nil
}

// ErrNotTrial is returned when a trial drive is asked of a shared drive.
var ErrNotTrial = errors.New("this is not a trial drive")

// RemoveLocal deletes a trial drive's index and files. It refuses anything
// that is not inside LocalRoot, so a damaged settings file cannot make it
// delete another folder.
func RemoveLocal(cfg config.Config) error {
	if !cfg.IsLocal() {
		return ErrNotTrial
	}
	dir := filepath.Dir(cfg.IndexPath)
	if filepath.Dir(dir) != filepath.Clean(LocalRoot()) {
		return fmt.Errorf("%s is not inside %s; it was not deleted", dir, LocalRoot())
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	os.Remove(LocalRoot()) // only if nothing else is there
	return nil
}
