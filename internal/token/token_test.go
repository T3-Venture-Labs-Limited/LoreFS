package token

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func sample() Token {
	return Token{Drive: "lorefs", Host: "x.proxy.rlwy.net", Port: 4321, DB: 1, TLS: true,
		Password: "pw-123", CA: "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n", UID: 1000}
}

func TestRoundTrip(t *testing.T) {
	s, err := Encode(sample())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, Prefix) {
		t.Fatalf("missing prefix: %s", s[:12])
	}
	got, err := Decode("  " + s + "\n")
	if err != nil {
		t.Fatal(err)
	}
	want := sample()
	want.V = 1
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestTruncated(t *testing.T) {
	s, _ := Encode(sample())
	if _, err := Decode(s[:len(s)/2]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

func TestWrongPrefix(t *testing.T) {
	s, _ := Encode(sample())
	if _, err := Decode("drive9_" + strings.TrimPrefix(s, Prefix)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if _, err := Decode("hello"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

func TestUnknownVersion(t *testing.T) {
	raw := `{"v":7,"host":"h","port":1,"password":"p","tls":false}`
	s := Prefix + base64.RawURLEncoding.EncodeToString([]byte(raw))
	_, err := Decode(s)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("want newer-version error, got %v", err)
	}
}

func TestMissingFields(t *testing.T) {
	for name, mut := range map[string]func(*Token){
		"host":     func(k *Token) { k.Host = "" },
		"port":     func(k *Token) { k.Port = 0 },
		"password": func(k *Token) { k.Password = "" },
		"ca":       func(k *Token) { k.CA = "" },
	} {
		k := sample()
		mut(&k)
		if _, err := Encode(k); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestPlainTokenNeedsNoCA(t *testing.T) {
	k := sample()
	k.TLS, k.CA = false, ""
	if _, err := Encode(k); err != nil {
		t.Fatal(err)
	}
}
