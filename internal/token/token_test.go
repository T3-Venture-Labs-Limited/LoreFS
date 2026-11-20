package token

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func sample() Token {
	return Token{Drive: "lorefs", Host: "index.example.net", Port: 4321, DB: 1, TLS: true,
		Password: "pw-123", CA: "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n", UID: 1000, GID: 1001}
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
	want.V = 2
	want.Scheme = "rediss"
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
	s := "lorefs2_" + base64.RawURLEncoding.EncodeToString([]byte(raw))
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

func TestFormat1CodeStillWorks(t *testing.T) {
	raw := `{"v":1,"drive":"lorefs","host":"h.example.net","port":6379,"db":1,"tls":true,"password":"pw","ca":"x","uid":1000}`
	got, err := Decode("lorefs1_" + base64.RawURLEncoding.EncodeToString([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Scheme != "rediss" || !got.TLS || got.UID != 1000 || got.GID != 0 || got.Host != "h.example.net" {
		t.Fatalf("%+v", got)
	}
	raw = `{"v":1,"host":"h","port":1,"db":1,"tls":false,"password":"pw","uid":5}`
	if got, err = Decode("lorefs1_" + base64.RawURLEncoding.EncodeToString([]byte(raw))); err != nil || got.Scheme != "redis" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestNewCodeCarriesSchemeAndGroup(t *testing.T) {
	s, _ := Encode(sample())
	if !strings.HasPrefix(s, "lorefs2_") {
		t.Fatal(s[:10])
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, "lorefs2_"))
	for _, want := range []string{`"scheme":"rediss"`, `"gid":1001`, `"v":2`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
}

func TestNewerFormatPrefixIsExplained(t *testing.T) {
	_, err := Decode("lorefs3_" + base64.RawURLEncoding.EncodeToString([]byte(`{"v":3}`)))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "newer version") || !strings.Contains(err.Error(), "update") {
		t.Fatalf("got %v", err)
	}
}

func TestTrialDriveHasNoCode(t *testing.T) {
	tk := sample()
	tk.Scheme = "sqlite3"
	if _, err := Encode(tk); !errors.Is(err, ErrTrialDrive) {
		t.Fatalf("got %v", err)
	}
}

func TestCutOffCodeIsRefused(t *testing.T) {
	s, _ := Encode(sample())
	for _, n := range []int{8, 20, len(s) - 3} {
		if _, err := Decode(s[:n]); !errors.Is(err, ErrInvalid) {
			t.Errorf("cut at %d: got %v", n, err)
		}
	}
}
