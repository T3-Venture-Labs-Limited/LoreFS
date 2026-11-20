package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/create"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/history"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/server"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/setup"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/ui"
)

const createHelp = `lorefs create makes a new shared drive on storage and an index you control,
or, with --local, a trial drive that lives on this computer only.

  lorefs create --bucket https://store.example.net/my-bucket \
                --access-key KEY --index rediss://index.example.net:6379/1

Secrets are never taken from the command line. Give the storage secret key
and the index password at the hidden prompts, in the environment variables
LOREFS_STORAGE_SECRET and LOREFS_INDEX_PASSWORD, or in a file readable only
by you (--secrets-file) holding lines such as LOREFS_STORAGE_SECRET=...
`

func cmdCreate(args []string) error {
	fs := flags("create")
	local := fs.Bool("local", false, "make a trial drive on this computer (no account, network or password)")
	name := fs.String("name", "", "the drive's name (default lorefs, or trial with --local)")
	bucket := fs.String("bucket", "", "address of the S3-compatible bucket, e.g. https://store.example.net/my-bucket")
	accessKey := fs.String("access-key", "", "the storage access key (or LOREFS_STORAGE_KEY)")
	indexURL := fs.String("index", "", "address of the index, e.g. rediss://index.example.net:6379/1")
	serverDir := fs.String("server-dir", "", "an index bundle made by `lorefs server init`: its address, password and certificate are used")
	caFile := fs.String("index-ca-file", "", "certificate authority file for an index that uses its own")
	secretsFile := fs.String("secrets-file", "", "file with LOREFS_STORAGE_SECRET and LOREFS_INDEX_PASSWORD lines")
	allowPlain := fs.Bool("allow-unencrypted-index", false, "accept an index without encryption (testing only)")
	noConnect := fs.Bool("no-connect", false, "do not connect this computer afterwards")
	connect := fs.Bool("connect", false, "connect this computer afterwards without asking")
	machine := fs.String("machine", "", "short name for this computer (default: its host name)")
	folder := fs.String("folder", "", "where the drive should appear (default ~/Lore)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, createHelp); fs.PrintDefaults() }
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if config.Joined() {
		cfg, _ := config.Load()
		return fmt.Errorf("this computer is already connected to the drive %q. Run `lorefs leave` first if you want to make another", cfg.DriveName)
	}
	if !*noConnect {
		if err := setup.CheckSession(); err != nil {
			fmt.Fprintf(os.Stderr, "%v.\n\n%s\nTo create the drive anyway without connecting this computer, add --no-connect.\n", err, setup.NoSessionAdvice)
			return exitCode(1)
		}
	}
	cfg := config.Defaults()
	if *folder != "" {
		cfg.Folder = *folder
	}
	var err error
	if cfg.Folder, err = filepath.Abs(cfg.Folder); err != nil {
		return err
	}
	if entries, rerr := os.ReadDir(cfg.Folder); rerr == nil && len(entries) > 0 && !jfs.IsMounted(cfg.Folder) {
		return fmt.Errorf("the folder %s already exists and is not empty; choose another with --folder", cfg.Folder)
	}
	say := func(s string) { fmt.Fprintln(os.Stderr, s) }
	ctx := context.Background()

	if *local {
		lc, err := create.CreateLocal(ctx, create.LocalOptions{Name: *name, Progress: say})
		if err != nil {
			return err
		}
		lc.Folder, lc.Machine = cfg.Folder, machineName(*machine)
		fmt.Fprintf(os.Stderr, "\nThe trial drive %q was created on this computer.\n", lc.DriveName)
		fmt.Fprintln(os.Stderr, "It cannot be joined from another computer. To share a drive, run `lorefs create` without --local.")
		if *noConnect {
			return nil
		}
		if err := setup.Connect(lc, "", nil, say); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "\nDone. The trial drive is at %s.\n", lc.Folder)
		return nil
	}

	// A shared drive: gather what is needed, then check and create.
	secrets := map[string]string{}
	if *secretsFile != "" {
		if secrets, err = readSecretsFile(*secretsFile); err != nil {
			return err
		}
	}
	pick := func(flagValue, env string) string {
		if flagValue != "" {
			return flagValue
		}
		if v := os.Getenv(env); v != "" {
			return v
		}
		return secrets[env]
	}
	interactive := ui.IsTerminal(os.Stdin)
	ask := func(v *string, question string, secret bool) error {
		if *v != "" {
			return nil
		}
		if !interactive {
			return nil
		}
		if secret {
			s, err := ui.AskSecret(question)
			*v = strings.TrimSpace(s)
			return err
		}
		*v = ui.Ask(question, "")
		return nil
	}
	opt := create.Options{Name: *name, AllowPlain: *allowPlain, Progress: say}
	if *serverDir != "" {
		if *indexURL != "" {
			return errUsage("give --server-dir or --index, not both")
		}
		conn, err := server.Read(*serverDir)
		if err != nil {
			return err
		}
		*indexURL, opt.IndexPass, opt.IndexCA = conn.IndexURL(1), conn.Password, conn.CA
	}
	opt.Storage.Bucket = *bucket
	opt.Storage.AccessKey = pick(*accessKey, "LOREFS_STORAGE_KEY")
	opt.Storage.SecretKey = pick("", "LOREFS_STORAGE_SECRET")
	opt.IndexURL = *indexURL
	if opt.IndexPass == "" {
		opt.IndexPass = pick("", "LOREFS_INDEX_PASSWORD")
	}
	if interactive {
		if opt.Storage.Bucket == "" {
			fmt.Fprintln(os.Stderr, "Where is the storage? Give the address of the bucket, e.g. https://store.example.net/my-bucket")
		}
		if err := ask(&opt.Storage.Bucket, "Bucket address", false); err != nil {
			return err
		}
		if err := ask(&opt.Storage.AccessKey, "Storage access key", false); err != nil {
			return err
		}
		if err := ask(&opt.Storage.SecretKey, "Storage secret key (not shown)", true); err != nil {
			return err
		}
		if err := ask(&opt.IndexURL, "Index address, e.g. rediss://index.example.net:6379/1", false); err != nil {
			return err
		}
		if err := ask(&opt.IndexPass, "Index password (not shown)", true); err != nil {
			return err
		}
	}
	var missing []string
	for _, m := range []struct{ v, what string }{
		{opt.Storage.Bucket, "--bucket"}, {opt.Storage.AccessKey, "--access-key or LOREFS_STORAGE_KEY"},
		{opt.Storage.SecretKey, "LOREFS_STORAGE_SECRET"}, {opt.IndexURL, "--index"}, {opt.IndexPass, "LOREFS_INDEX_PASSWORD"},
	} {
		if m.v == "" {
			missing = append(missing, m.what)
		}
	}
	if len(missing) > 0 {
		return errUsage("missing " + strings.Join(missing, ", ") + "\n\n" + createHelp)
	}
	if *caFile != "" {
		if opt.IndexCA, err = os.ReadFile(*caFile); err != nil {
			return fmt.Errorf("reading the certificate authority file: %w", err)
		}
	}

	res, err := create.Create(ctx, opt)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nThe drive %q was created.\n\nJoin code for other computers (one line):\n\n", res.Config.DriveName)
	fmt.Println(res.Code)
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Treat this like a password. Anyone who has it can read, change and delete")
	fmt.Fprintln(os.Stderr, "every file on the drive. Send it through a private channel. You can print it")
	fmt.Fprintln(os.Stderr, "again later with `lorefs invite`.")

	if *noConnect {
		return nil
	}
	if !*connect && !(interactive && ui.Confirm("\nConnect this computer to the drive now?")) {
		fmt.Fprintln(os.Stderr, "\nTo connect this computer later, run `lorefs join` and paste the code.")
		return nil
	}
	res.Config.Folder, res.Config.Machine = cfg.Folder, machineName(*machine)
	if err := setup.Connect(res.Config, res.Pass, res.CA, say); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nDone. The shared drive %q is at %s.\n", res.Config.DriveName, res.Config.Folder)
	return nil
}

func machineName(given string) string {
	if given == "" {
		given, _ = os.Hostname()
	}
	return history.CleanMachine(given)
}

// readSecretsFile reads KEY=value lines. The file must not be readable by
// anyone but its owner.
func readSecretsFile(path string) (map[string]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s can be read by other users. Run: chmod 600 %s", path, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, errors.New(path + ": each line should look like KEY=value")
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out, nil
}
