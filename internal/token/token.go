// Package token encodes and decodes the join token: the one string that
// carries everything a second machine needs to connect to a drive.
//
// A token is a secret. Anyone holding it can read and delete every file.
package token

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Prefix marks a join code of the current format. Codes of format 1,
// made by the program that came before, are still accepted.
const (
	Prefix = "lorefs2_"
)

// Token is the decoded content of a join token.
type Token struct {
	V        int    `json:"v"`
	Drive    string `json:"drive"`
	Scheme   string `json:"scheme,omitempty"` // "rediss" or "redis"; empty in format 1
	Host     string `json:"host"`
	Port     int    `json:"port"`
	DB       int    `json:"db"`
	TLS      bool   `json:"tls"`
	Password string `json:"password"`
	CA       string `json:"ca,omitempty"`  // PEM certificate authority
	UID      int    `json:"uid"`           // user that owns the drive's files
	GID      int    `json:"gid,omitempty"` // group that owns them; 0 in format 1
}

// ErrInvalid is wrapped by every decoding error.
var ErrInvalid = errors.New("not a valid join token")

// ErrTrialDrive is returned when asked to encode a drive whose index is a
// file on one computer: nobody else can reach it.
var ErrTrialDrive = errors.New("a trial drive lives on this computer only, so it has no join code")

// Encode turns a Token into the string a person copies.
func Encode(t Token) (string, error) {
	t.V = 2
	if t.Scheme == "sqlite3" {
		return "", ErrTrialDrive
	}
	if t.Scheme == "" {
		t.Scheme = "redis"
		if t.TLS {
			t.Scheme = "rediss"
		}
	}
	if err := t.check(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode parses and validates a join token of format 1 or 2.
func Decode(s string) (Token, error) {
	var t Token
	s = strings.TrimSpace(s)
	body, v, ok := splitPrefix(s)
	if !ok {
		return t, fmt.Errorf("%w: it should start with %q", ErrInvalid, Prefix)
	}
	if v > 2 {
		return t, fmt.Errorf("%w: it was made by a newer version of lorefs (format %d); update lorefs and try again", ErrInvalid, v)
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return t, fmt.Errorf("%w: it looks cut off or mistyped", ErrInvalid)
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, fmt.Errorf("%w: it looks cut off or mistyped", ErrInvalid)
	}
	if t.V > 2 {
		return t, fmt.Errorf("%w: it was made by a newer version of lorefs (format %d); update lorefs and try again", ErrInvalid, t.V)
	}
	if t.V < 1 {
		return t, fmt.Errorf("%w: it looks cut off or mistyped", ErrInvalid)
	}
	if t.Scheme == "" {
		t.Scheme = "redis"
		if t.TLS {
			t.Scheme = "rediss"
		}
	}
	t.TLS = t.Scheme == "rediss"
	if err := t.check(); err != nil {
		return t, err
	}
	return t, nil
}

// splitPrefix recognises "lorefs<digits>_" and returns the rest and the
// format number.
func splitPrefix(s string) (body string, v int, ok bool) {
	rest, found := strings.CutPrefix(s, "lorefs")
	if !found {
		return "", 0, false
	}
	n := strings.IndexByte(rest, '_')
	if n < 1 {
		return "", 0, false
	}
	v, err := strconv.Atoi(rest[:n])
	if err != nil || v < 1 {
		return "", 0, false
	}
	return rest[n+1:], v, true
}

func (t Token) check() error {
	switch {
	case t.Host == "":
		return fmt.Errorf("%w: no index address", ErrInvalid)
	case t.Port <= 0 || t.Port > 65535:
		return fmt.Errorf("%w: bad index port", ErrInvalid)
	case t.Password == "":
		return fmt.Errorf("%w: no password", ErrInvalid)
	}
	return nil
}
