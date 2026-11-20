// Package update brings the lorefs program on this computer up to date from
// published releases: it reads the list of checksums of the newest release,
// downloads this computer's archive, verifies it, and runs the archive's
// own installer. Nothing is changed unless the archive checks out.
package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultReleaseURL is where the newest release's files are published.
const DefaultReleaseURL = "https://github.com/T3-Venture-Labs-Limited/LoreFS/releases/latest/download"

// ReleaseURL is where to look for the newest release. LOREFS_RELEASE_URL
// overrides it (a mirror, or a folder served over HTTP for testing).
func ReleaseURL() string {
	if u := os.Getenv("LOREFS_RELEASE_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return DefaultReleaseURL
}

// Release is one archive of the newest release.
type Release struct {
	Version string // without a leading "v"
	Archive string // file name
	SHA256  string
}

var archiveRe = regexp.MustCompile(`^lorefs-(.+)-linux-(amd64|arm64)\.tar\.gz$`)

// Arch returns the release architecture name for this computer.
func Arch() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH, nil
	}
	return "", fmt.Errorf("there are no releases for this processor (%s)", runtime.GOARCH)
}

// ParseSums finds the archive for an architecture in the text of a
// SHA256SUMS file ("<hash>  <file name>" per line). With several versions
// listed, the highest wins.
func ParseSums(sums, arch string) (Release, error) {
	var best Release
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		m := archiveRe.FindStringSubmatch(strings.TrimPrefix(f[1], "*"))
		if m == nil || m[2] != arch || len(f[0]) != 64 {
			continue
		}
		r := Release{Version: strings.TrimPrefix(m[1], "v"), Archive: strings.TrimPrefix(f[1], "*"), SHA256: strings.ToLower(f[0])}
		if best.Archive == "" || Newer(best.Version, r.Version) {
			best = r
		}
	}
	if best.Archive == "" {
		return best, fmt.Errorf("the release lists no archive for %s", arch)
	}
	return best, nil
}

var (
	versionRe  = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)
	describeRe = regexp.MustCompile(`-\d+-g[0-9a-f]+(-dirty)?$`)
)

type semver struct {
	major, minor, patch int
	pre                 string
	ok                  bool
}

func parse(v string) semver {
	v = strings.TrimSpace(v)
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return semver{}
	}
	n := func(s string) int { x, _ := strconv.Atoi(s); return x }
	return semver{n(m[1]), n(m[2]), n(m[3]), m[4], true}
}

// compare returns -1, 0 or 1. A version with a pre-release part (rc.1) is
// older than the same version without one.
func compare(a, b semver) int {
	for _, p := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	// Compare dot-separated parts: numbers as numbers.
	as, bs := strings.Split(a.pre, "."), strings.Split(b.pre, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, ex := strconv.Atoi(as[i])
		y, ey := strconv.Atoi(bs[i])
		switch {
		case ex == nil && ey == nil && x != y:
			if x < y {
				return -1
			}
			return 1
		case as[i] != bs[i] && (ex != nil || ey != nil):
			if as[i] < bs[i] {
				return -1
			}
			return 1
		}
	}
	return len(as) - len(bs)
}

// Newer reports whether candidate is a newer release than installed.
// An installed version that is not a release number (a build from source
// such as "dev" or "abc1234") is older than any release. A version built
// after a release ("v0.2.0-3-gabc1234") counts as that release or later, so
// it is not offered the release it was built from.
func Newer(installed, candidate string) bool {
	c := parse(candidate)
	if !c.ok {
		return false
	}
	installed = strings.TrimSpace(installed)
	if d := describeRe.FindString(installed); d != "" {
		if i := parse(strings.TrimSuffix(installed, d)); i.ok {
			return compare(i, c) < 0 // built after its tag: only a later release is newer
		}
	}
	i := parse(installed)
	if !i.ok {
		return true
	}
	return compare(i, c) < 0
}

// Options control Run.
type Options struct {
	Installed string
	CheckOnly bool
	Base      string       // release address; default ReleaseURL()
	Out       io.Writer    // progress and the installer's output
	HTTP      *http.Client // default: 60 s per request
	Arch      string       // default: this computer's
}

// Result says what Run found and did.
type Result struct {
	Installed string // version that was running
	Latest    string // newest release
	Newer     bool
	Done      bool // the archive was installed
}

func (o *Options) say(format string, a ...any) {
	if o.Out != nil {
		fmt.Fprintf(o.Out, format+"\n", a...)
	}
}

func (o *Options) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than expected", url)
	}
	return data, nil
}

// Run looks for a newer release and, unless CheckOnly is set, installs it.
// It changes nothing on this computer unless the downloaded archive matches
// the published checksum.
func Run(ctx context.Context, o Options) (Result, error) {
	res := Result{Installed: o.Installed}
	base := o.Base
	if base == "" {
		base = ReleaseURL()
	}
	arch := o.Arch
	if arch == "" {
		var err error
		if arch, err = Arch(); err != nil {
			return res, err
		}
	}
	sums, err := o.get(ctx, base+"/SHA256SUMS", 1<<20)
	if err != nil {
		return res, fmt.Errorf("could not reach the releases (%w); nothing was changed", err)
	}
	rel, err := ParseSums(string(sums), arch)
	if err != nil {
		return res, err
	}
	res.Latest = rel.Version
	res.Newer = Newer(o.Installed, rel.Version)
	if !res.Newer || o.CheckOnly {
		return res, nil
	}

	o.say("Downloading lorefs %s...", rel.Version)
	data, err := o.get(ctx, base+"/"+rel.Archive, 512<<20)
	if err != nil {
		return res, fmt.Errorf("the download failed (%w); nothing was changed", err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != rel.SHA256 {
		return res, errors.New("the downloaded file does not match its published checksum, so it was not installed; nothing was changed")
	}
	dir, err := os.MkdirTemp("", "lorefs-update-*")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(dir)
	root, err := extract(data, dir)
	if err != nil {
		return res, fmt.Errorf("the downloaded file could not be unpacked (%w); nothing was changed", err)
	}
	installer := filepath.Join(root, "install.sh")
	if _, err := os.Stat(installer); err != nil {
		return res, errors.New("the release has no installer; nothing was changed")
	}
	o.say("Installing...")
	cmd := exec.CommandContext(ctx, installer)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = o.Out, o.Out
	if err := cmd.Run(); err != nil {
		return res, fmt.Errorf("the installer failed (%w); the installed program was not changed by that step", err)
	}
	res.Done = true
	return res, nil
}

// extract unpacks a .tar.gz into dir and returns the single top folder.
func extract(data []byte, dir string) (string, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	top := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		name := filepath.Clean(h.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("unsafe path %q", h.Name)
		}
		first := strings.SplitN(name, string(filepath.Separator), 2)[0]
		if top == "" {
			top = first
		} else if top != first {
			return "", errors.New("the archive has more than one top folder")
		}
		target := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777|0o600)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return "", err
			}
			f.Close()
		}
	}
	if top == "" {
		return "", errors.New("the archive is empty")
	}
	return filepath.Join(dir, top), nil
}
