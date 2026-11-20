package server

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func verify(t *testing.T, ca *Authority, sc *ServerCert, host string) error {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM) {
		t.Fatal("authority not readable")
	}
	b, _ := pem.Decode(sc.CertPEM)
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: host, CurrentTime: time.Now()})
	return err
}

func TestCertificateIsValidForItsAddressOnly(t *testing.T) {
	ca, err := NewAuthority("team", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ addr, other string }{
		{"203.0.113.7", "203.0.113.8"},
		{"index.example.net", "other.example.net"},
	} {
		sc, err := ca.Issue(tc.addr, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := verify(t, ca, sc, tc.addr); err != nil {
			t.Errorf("%s: should verify: %v", tc.addr, err)
		}
		if err := verify(t, ca, sc, tc.other); err == nil {
			t.Errorf("%s: must not verify for %s", tc.addr, tc.other)
		}
	}
}

func TestCertificateFromAnotherAuthorityIsRefused(t *testing.T) {
	a, _ := NewAuthority("a", time.Now())
	b, _ := NewAuthority("b", time.Now())
	sc, _ := a.Issue("203.0.113.7", time.Now())
	if verify(t, b, sc, "203.0.113.7") == nil {
		t.Fatal("a certificate signed by a different authority must not verify")
	}
}

func TestAuthorityRoundTripsAndStillSigns(t *testing.T) {
	ca, _ := NewAuthority("team", time.Now())
	again, err := LoadAuthority(ca.CertPEM, ca.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := again.Issue("index.example.net", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := verify(t, ca, sc, "index.example.net"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthority([]byte("junk"), []byte("junk")); err == nil {
		t.Fatal("junk must be refused")
	}
}

func TestExpiredCertificateIsRefused(t *testing.T) {
	ca, _ := NewAuthority("team", time.Now().Add(-20*365*24*time.Hour))
	sc, _ := ca.Issue("203.0.113.7", time.Now().Add(-20*365*24*time.Hour))
	if verify(t, ca, sc, "203.0.113.7") == nil {
		t.Fatal("a certificate that ran out must not verify")
	}
}

func TestPasswordsAreLongAndDiffer(t *testing.T) {
	a, _ := Password()
	b, _ := Password()
	if len(a) != 43 || a == b || strings.ContainsAny(a, "+/= \n") {
		t.Fatalf("%q %q", a, b)
	}
}
