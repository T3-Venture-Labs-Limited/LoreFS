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
	"strings"
)

// Prefix marks a LoreFS join token and its format version.
const Prefix = "lorefs1_"

// Token is the decoded content of a join token.
type Token struct {
	V        int    `json:"v"`
	Drive    string `json:"drive"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	DB       int    `json:"db"`
	TLS      bool   `json:"tls"`
	Password string `json:"password"`
	CA       string `json:"ca,omitempty"` // PEM certificate authority
	UID      int    `json:"uid"`          // owner of the drive's files
}

// ErrInvalid is wrapped by every decoding error.
var ErrInvalid = errors.New("not a valid join token")

// Encode turns a Token into the string a person copies.
func Encode(t Token) (string, error) {
	t.V = 1
	if err := t.check(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode parses and validates a join token.
func Decode(s string) (Token, error) {
	var t Token
	s = strings.TrimSpace(s)
	body, ok := strings.CutPrefix(s, Prefix)
	if !ok {
		return t, fmt.Errorf("%w: it should start with %q", ErrInvalid, Prefix)
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return t, fmt.Errorf("%w: it looks cut off or mistyped", ErrInvalid)
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, fmt.Errorf("%w: it looks cut off or mistyped", ErrInvalid)
	}
	if t.V != 1 {
		return t, fmt.Errorf("%w: it was made by a newer version of lorefs (format %d)", ErrInvalid, t.V)
	}
	if err := t.check(); err != nil {
		return t, err
	}
	return t, nil
}

func (t Token) check() error {
	switch {
	case t.Host == "":
		return fmt.Errorf("%w: no index address", ErrInvalid)
	case t.Port <= 0 || t.Port > 65535:
		return fmt.Errorf("%w: bad index port", ErrInvalid)
	case t.Password == "":
		return fmt.Errorf("%w: no password", ErrInvalid)
	case t.TLS && t.CA == "":
		return fmt.Errorf("%w: no certificate", ErrInvalid)
	}
	return nil
}
