// Package id generates the opaque identifiers, secrets, and join codes the service hands out, and
// hashes secrets for at-rest storage. Secrets are high-entropy server-generated tokens (not
// user-chosen passwords), so a salted SHA-256 is sufficient and keeps the build dependency-free —
// no bcrypt/argon2 module required.
package id

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
)

// crockford-ish base32 without padding, uppercase — friendly for humans to read/share join codes.
var codeEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// New returns a URL-safe opaque identifier (160 bits) for accounts, leagues, etc.
func New() string {
	return token(20)
}

// Short returns a short (64-bit) URL-safe id for non-security uses like request ids in access logs.
func Short() string {
	return token(8)
}

// Secret returns a 256-bit secret token (hex) shown to the client exactly once at issue time.
func Secret() string {
	b := mustRand(32)
	return hex.EncodeToString(b)
}

// Code returns a short, shareable join/friend code (e.g. "K7Q2-9F3M"), 50 bits of entropy.
func Code() string {
	raw := codeEnc.EncodeToString(mustRand(7)) // 7 bytes -> ~11 base32 chars, take 8
	c := raw[:8]
	return c[:4] + "-" + c[4:]
}

// Hash returns a hex SHA-256 of salt||secret. Pair with a per-record random Salt.
func Hash(salt, secret string) string {
	h := sha256.New()
	h.Write([]byte(salt))
	h.Write([]byte(secret))
	return hex.EncodeToString(h.Sum(nil))
}

// Salt returns a fresh 128-bit hex salt.
func Salt() string {
	return hex.EncodeToString(mustRand(16))
}

// Verify reports whether secret matches the stored salt+hash, in constant time.
func Verify(salt, storedHash, secret string) bool {
	want, err := hex.DecodeString(storedHash)
	if err != nil {
		return false
	}
	got, err := hex.DecodeString(Hash(salt, secret))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(want, got) == 1
}

func token(n int) string {
	return hex.EncodeToString(mustRand(n))
}

// mustRand panics only if the OS CSPRNG is unavailable, which is a fatal environment fault, not a
// recoverable runtime condition — there is no safe way to mint identifiers without it.
func mustRand(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("id: crypto/rand unavailable: " + err.Error())
	}
	return b
}
