package jfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DriveStatus is what `juicefs status` reports about a drive. It works
// with every kind of index, so it is the one source for facts that do not
// depend on the engine: the drive's name, who is connected, whether the
// change log is on.
type DriveStatus struct {
	Name, Storage, Bucket string
	ChangeLog             bool
	ChangeLogMaxAge       time.Duration
	ChangeLogMaxLines     int
	TrashDays             int
	UsedSpace             int64
	UsedInodes            int64
	Sessions              []Session
}

// Session is one mounted computer.
type Session struct {
	ID         uint64
	Host       string
	MountPoint string
	Version    string
	MountTime  time.Time
	Expire     time.Time
}

// ParseStatus reads the JSON that `juicefs status` prints on standard
// output. Log lines on standard error must not be passed in.
func ParseStatus(out []byte) (DriveStatus, error) {
	var raw struct {
		Setting struct {
			Name              string
			Storage           string
			Bucket            string
			TrashDays         int
			ChangeLog         bool
			ChangeLogMaxAge   int64 // seconds
			ChangeLogMaxLines int
		}
		Sessions []struct {
			Sid        uint64
			Expire     time.Time
			Version    string
			HostName   string
			MountPoint string
			MountTime  time.Time
		}
		Stat struct {
			UsedSpace  int64
			UsedInodes int64
		}
	}
	// Some versions print log lines before the JSON; start at the first
	// line that opens it.
	if i := bytes.Index(out, []byte("{\n")); i > 0 {
		out = out[i:]
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return DriveStatus{}, fmt.Errorf("the drive's status could not be read: %w", err)
	}
	if raw.Setting.Name == "" {
		return DriveStatus{}, errors.New("the index holds no drive")
	}
	s := DriveStatus{
		Name: raw.Setting.Name, Storage: raw.Setting.Storage, Bucket: raw.Setting.Bucket,
		ChangeLog: raw.Setting.ChangeLog, ChangeLogMaxLines: raw.Setting.ChangeLogMaxLines,
		ChangeLogMaxAge: time.Duration(raw.Setting.ChangeLogMaxAge) * time.Second,
		TrashDays:       raw.Setting.TrashDays, UsedSpace: raw.Stat.UsedSpace, UsedInodes: raw.Stat.UsedInodes,
	}
	for _, x := range raw.Sessions {
		s.Sessions = append(s.Sessions, Session{ID: x.Sid, Host: x.HostName, MountPoint: x.MountPoint,
			Version: x.Version, MountTime: x.MountTime, Expire: x.Expire})
	}
	return s, nil
}

// Status asks juicefs about the drive at metaURL. password, if not empty,
// travels in the environment, never on the command line.
func (e Exec) Status(ctx context.Context, metaURL, password string) (DriveStatus, error) {
	cmd := exec.CommandContext(ctx, e.Bin, "status", metaURL)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	env := os.Environ()
	if password != "" {
		env = append(env, "META_PASSWORD="+password)
	}
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		return DriveStatus{}, fmt.Errorf("juicefs status: %w: %s", err, lastLine(stderr.String()))
	}
	return ParseStatus(stdout.Bytes())
}

// OtherSessions returns the sessions that are not at the given mount point
// on the given host (this computer's own).
func (s DriveStatus) OtherSessions(host, mountPoint string) []Session {
	var out []Session
	for _, x := range s.Sessions {
		if strings.EqualFold(x.Host, host) && x.MountPoint == mountPoint {
			continue
		}
		out = append(out, x)
	}
	return out
}
