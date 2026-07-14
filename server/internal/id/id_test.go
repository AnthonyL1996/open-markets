package id

import (
	"regexp"
	"testing"
)

func TestNewIsUniqueAndHex(t *testing.T) {
	seen := map[string]bool{}
	re := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for i := 0; i < 1000; i++ {
		v := New()
		if !re.MatchString(v) {
			t.Fatalf("New() = %q, not 40 hex chars", v)
		}
		if seen[v] {
			t.Fatalf("New() collision at %q", v)
		}
		seen[v] = true
	}
}

func TestCodeFormat(t *testing.T) {
	re := regexp.MustCompile(`^[A-Z2-7]{4}-[A-Z2-7]{4}$`)
	for i := 0; i < 100; i++ {
		c := Code()
		if !re.MatchString(c) {
			t.Fatalf("Code() = %q, want XXXX-XXXX base32", c)
		}
	}
}

func TestHashVerifyRoundTrip(t *testing.T) {
	salt := Salt()
	secret := Secret()
	h := Hash(salt, secret)
	if !Verify(salt, h, secret) {
		t.Fatal("Verify rejected the correct secret")
	}
	if Verify(salt, h, secret+"x") {
		t.Fatal("Verify accepted a wrong secret")
	}
	if Verify(Salt(), h, secret) {
		t.Fatal("Verify accepted under a different salt")
	}
}

func TestVerifyRejectsGarbageHash(t *testing.T) {
	if Verify("aa", "not-hex", "secret") {
		t.Fatal("Verify accepted a non-hex stored hash")
	}
}
