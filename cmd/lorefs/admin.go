package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/config"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/create"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/index"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/jfs"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/mount"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/status"
	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/token"
)

// cmdMakeToken builds the first join code of a new drive. It is used once
// by infra/create-backend.sh; afterwards `lorefs invite` does the same from
// a joined computer. The password is read from the environment so that it
// never appears on a command line.
func cmdMakeToken(args []string) error {
	fs := flags("make-token")
	host := fs.String("host", "", "index host")
	port := fs.Int("port", 0, "index port")
	db := fs.Int("db", 1, "index database number")
	drive := fs.String("drive", "lorefs", "drive name")
	caFile := fs.String("ca-file", "", "certificate authority file (PEM)")
	noTLS := fs.Bool("no-tls", false, "for local testing only")
	uid := fs.Int("uid", os.Getuid(), "numeric id of the user who owns the drive's files")
	gid := fs.Int("gid", os.Getgid(), "numeric id of the group that owns them")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	pw := strings.TrimSpace(os.Getenv("META_PASSWORD"))
	if pw == "" {
		return errUsage("set META_PASSWORD in the environment")
	}
	t := token.Token{Drive: *drive, Host: *host, Port: *port, DB: *db, TLS: !*noTLS, Password: pw, UID: *uid, GID: *gid}
	if t.TLS {
		ca, err := os.ReadFile(*caFile)
		if err != nil {
			return fmt.Errorf("reading the certificate authority: %w", err)
		}
		t.CA = string(ca)
	}
	code, err := token.Encode(t)
	if err != nil {
		return err
	}
	fmt.Println(code)
	return nil
}

// cmdIndexCheck tests a login to an index directly, without a mount. The
// backend scripts use it to verify a new index and to measure its
// response time. The password is read from the environment.
func cmdIndexCheck(args []string) error {
	fs := flags("index-check")
	host := fs.String("host", "", "index host")
	port := fs.Int("port", 0, "index port")
	db := fs.Int("db", 1, "index database number")
	caFile := fs.String("ca-file", "", "certificate authority file (PEM)")
	noTLS := fs.Bool("no-tls", false, "connect without encryption (should be refused)")
	pings := fs.Int("pings", 5, "how many round trips to time")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	t := index.Target{Host: *host, Port: *port, DB: *db, TLS: !*noTLS, Password: strings.TrimSpace(os.Getenv("META_PASSWORD"))}
	if t.TLS {
		ca, err := os.ReadFile(*caFile)
		if err != nil {
			return fmt.Errorf("reading the certificate authority: %w", err)
		}
		t.CAPEM = ca
	}
	start := time.Now()
	conn, err := index.Dial(t, 15*time.Second)
	if err != nil {
		fmt.Printf("refused: %v\n", err)
		return exitCode(1)
	}
	defer conn.Close()
	fmt.Printf("connected and logged in (%d ms including the TLS handshake)\n", time.Since(start).Milliseconds())
	var best, total time.Duration
	for i := 0; i < *pings; i++ {
		d, err := conn.Ping()
		if err != nil {
			return err
		}
		total += d
		if best == 0 || d < best {
			best = d
		}
	}
	if *pings > 0 {
		fmt.Printf("round trip: best %.1f ms, average %.1f ms over %d requests\n",
			float64(best.Microseconds())/1000, float64(total.Microseconds())/1000/float64(*pings), *pings)
	}
	used, limit, _ := conn.Memory()
	fmt.Printf("memory: %s used, limit %s\n", status.Size(used), status.Size(limit))
	if name, ok := conn.IsFormatted(); ok {
		fmt.Printf("holds the drive %q\n", name)
	} else {
		fmt.Println("holds no drive yet")
	}
	return nil
}

// cmdAdmin holds the few settings of the drive itself that a person may
// need to change.
func cmdAdmin(args []string) error {
	const use = "use: lorefs admin changelog on|off|status"
	if len(args) < 1 || args[0] != "changelog" {
		return errUsage(use)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var env []string
	if !cfg.IsLocal() {
		pw, err := config.Password()
		if err != nil {
			return err
		}
		env = []string{"META_PASSWORD=" + pw}
	}
	if len(args) == 2 && args[1] == "status" {
		ds, err := jfs.NewExec().Status(context.Background(), mount.IndexURL(cfg), strings.TrimPrefix(strings.Join(env, ""), "META_PASSWORD="))
		if err != nil {
			return err
		}
		if ds.ChangeLog {
			fmt.Printf("The change log is on: it keeps %s of changes (up to %d entries).\n", status.Age(ds.ChangeLogMaxAge), ds.ChangeLogMaxLines)
		} else {
			fmt.Println("The change log is off. Versions are recorded by a periodic walk of the changed files.")
		}
		return nil
	}
	if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
		return errUsage(use)
	}
	on := args[1] == "on"
	if err := create.SetChangeLog(context.Background(), nil, mount.IndexURL(cfg), env, on); err != nil {
		return err
	}
	if on {
		fmt.Println("The change log is on. Within a few minutes every computer follows it to record versions as they happen.")
	} else {
		fmt.Println("The change log is off. Versions are recorded by a periodic walk, up to ten minutes after a save.")
		fmt.Println("Turn it on again with: lorefs admin changelog on")
	}
	return nil
}
