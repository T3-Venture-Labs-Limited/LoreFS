// Package index talks to the drive's index (a Redis server) just enough
// to check a login, measure the response time and read a few numbers for
// the status page. File operations never go through this package; they
// go through the JuiceFS mount.
package index

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Target says where the index is and how to log in.
type Target struct {
	Host     string
	Port     int
	DB       int
	TLS      bool
	Password string
	CAPEM    []byte // certificate authority the server must be signed by
}

// Errors that callers turn into plain-language messages.
var (
	ErrUnreachable = errors.New("the index cannot be reached")
	ErrCertificate = errors.New("the index did not prove its identity")
	ErrPassword    = errors.New("the index rejected the password")
)

// Conn is one logged-in connection.
type Conn struct {
	c  net.Conn
	r  *bufio.Reader
	db int // the drive's database, selected after login
}

// Dial connects, verifies the server's certificate, logs in and selects
// the drive's database.
func Dial(t Target, timeout time.Duration) (*Conn, error) {
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	d := &net.Dialer{Timeout: timeout}
	var c net.Conn
	var err error
	if t.TLS {
		// No certificate given: trust the computer's own list of authorities.
		var pool *x509.CertPool
		if len(t.CAPEM) > 0 {
			pool = x509.NewCertPool()
			if !pool.AppendCertsFromPEM(t.CAPEM) {
				return nil, fmt.Errorf("%w: the stored certificate is unreadable", ErrCertificate)
			}
		}
		c, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{
			RootCAs: pool, ServerName: t.Host, MinVersion: tls.VersionTLS12,
		})
		if err != nil {
			var certErr *tls.CertificateVerificationError
			if errors.As(err, &certErr) {
				return nil, fmt.Errorf("%w: %v", ErrCertificate, certErr.Err)
			}
			return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
		}
	} else {
		c, err = d.Dial("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
		}
	}
	conn := &Conn{c: c, r: bufio.NewReader(c), db: t.DB}
	c.SetDeadline(time.Now().Add(timeout))
	if t.Password != "" {
		if _, err := conn.Do("AUTH", t.Password); err != nil {
			c.Close()
			if isServerError(err) {
				return nil, ErrPassword
			}
			return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
		}
	}
	if _, err := conn.Do("SELECT", strconv.Itoa(t.DB)); err != nil {
		c.Close()
		if isServerError(err) {
			return nil, ErrPassword // NOAUTH: a password is required
		}
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	c.SetDeadline(time.Time{})
	return conn, nil
}

// Close ends the connection.
func (c *Conn) Close() error { return c.c.Close() }

type serverError string

func (e serverError) Error() string { return string(e) }

func isServerError(err error) bool {
	var se serverError
	return errors.As(err, &se)
}

// Do sends one command and returns its reply: a string, an int64, nil,
// or a slice of those.
func (c *Conn) Do(args ...string) (any, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&sb, "$%d\r\n%s\r\n", len(a), a)
	}
	c.c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c.c, sb.String()); err != nil {
		return nil, err
	}
	return c.read()
}

func (c *Conn) read() (any, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, errors.New("empty reply")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, serverError(line[1:])
	case ':':
		return strconv.ParseInt(line[1:], 10, 64)
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(c.r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, err
		}
		if n < 0 {
			return nil, nil
		}
		out := make([]any, n)
		for i := range out {
			if out[i], err = c.read(); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unexpected reply %q", line)
}

// Ping measures one round trip on an open connection.
func (c *Conn) Ping() (time.Duration, error) {
	start := time.Now()
	if _, err := c.Do("PING"); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

// Memory returns the bytes the index uses and its limit (0 = no limit).
func (c *Conn) Memory() (used, limit int64, err error) {
	reply, err := c.Do("INFO", "memory")
	if err != nil {
		return 0, 0, err
	}
	text, _ := reply.(string)
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch k {
		case "used_memory":
			used, _ = strconv.ParseInt(v, 10, 64)
		case "maxmemory":
			limit, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return used, limit, nil
}

// LastBackup returns when a client last wrote a copy of the index into
// the storage bucket. ok is false if no copy has been written yet.
func (c *Conn) LastBackup() (t time.Time, ok bool, err error) {
	// JuiceFS keeps this as an attribute of the top folder (inode 1).
	reply, err := c.Do("HGET", "x1", "lastBackup")
	if err != nil {
		return t, false, err
	}
	s, _ := reply.(string)
	if s == "" {
		return t, false, nil
	}
	t, err = time.Parse(time.RFC3339, s)
	if err != nil {
		return t, false, nil
	}
	return t, true, nil
}

// FileBackup is the outcome of the last copy of the drive's bucket to a
// second provider, as the server's backup job records it.
type FileBackup struct {
	Time        time.Time `json:"time"`
	OK          bool      `json:"ok"`
	Error       string    `json:"error"`
	Objects     int64     `json:"objects"`
	Bytes       int64     `json:"bytes"`
	Destination string    `json:"destination"`
}

// FileBackupKey is where the backup job leaves its result: database 2 of
// the index, away from the drive's own data in database 1.
const (
	fileBackupDB  = "2"
	FileBackupKey = "lorefs:backup"
)

// LastFileBackup returns the last recorded copy of the bucket. ok is false
// when no backup job has ever reported (a drive without one).
func (c *Conn) LastFileBackup() (b FileBackup, ok bool, err error) {
	if _, err = c.Do("SELECT", fileBackupDB); err != nil {
		return b, false, err
	}
	reply, getErr := c.Do("GET", FileBackupKey)
	if _, err = c.Do("SELECT", strconv.Itoa(c.db)); err != nil {
		return b, false, err
	}
	if getErr != nil {
		return b, false, getErr
	}
	s, _ := reply.(string)
	if s == "" {
		return b, false, nil
	}
	if err := json.Unmarshal([]byte(s), &b); err != nil {
		return b, false, fmt.Errorf("the backup record is unreadable: %w", err)
	}
	return b, true, nil
}

// KeyCount returns how many keys the selected database holds. A database
// that is not empty is not a safe place to create a drive.
func (c *Conn) KeyCount() (int64, error) {
	reply, err := c.Do("DBSIZE")
	if err != nil {
		return 0, err
	}
	n, _ := reply.(int64)
	return n, nil
}

// IsFormatted reports whether the selected database holds a JuiceFS
// filesystem, and returns its name.
func (c *Conn) IsFormatted() (name string, ok bool) {
	reply, err := c.Do("GET", "setting")
	if err != nil {
		return "", false
	}
	s, _ := reply.(string)
	if s == "" {
		return "", false
	}
	if i := strings.Index(s, `"Name"`); i >= 0 {
		rest := s[i+len(`"Name"`):]
		if a := strings.IndexByte(rest, '"'); a >= 0 {
			if b := strings.IndexByte(rest[a+1:], '"'); b >= 0 {
				name = rest[a+1 : a+1+b]
			}
		}
	}
	return name, true
}

// DriveSetting is what JuiceFS keeps about a drive in its index: where the
// files are stored and the key to reach them (every computer of the drive
// receives it when it mounts).
type DriveSetting struct {
	Name      string `json:"Name"`
	Storage   string `json:"Storage"`
	Bucket    string `json:"Bucket"`
	AccessKey string `json:"AccessKey"`
	SecretKey string `json:"SecretKey"`
}

// Setting reads the drive's settings from the selected database.
func (c *Conn) Setting() (DriveSetting, error) {
	var s DriveSetting
	reply, err := c.Do("GET", "setting")
	if err != nil {
		return s, err
	}
	text, _ := reply.(string)
	if text == "" {
		return s, errors.New("the index holds no drive")
	}
	if err := json.Unmarshal([]byte(text), &s); err != nil {
		return s, fmt.Errorf("the drive's settings are unreadable: %w", err)
	}
	return s, nil
}

// SaveFileBackup records the outcome of a copy of the drive's bucket where
// every computer's status page can read it.
func (c *Conn) SaveFileBackup(b FileBackup) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if _, err = c.Do("SELECT", fileBackupDB); err != nil {
		return err
	}
	_, setErr := c.Do("SET", FileBackupKey, string(data))
	if _, err = c.Do("SELECT", strconv.Itoa(c.db)); err != nil {
		return err
	}
	return setErr
}
