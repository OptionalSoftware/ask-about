package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
)

// tokenAlphabet is lower-case base32 without padding: unambiguous when read
// aloud or retyped, and safe in a URL path without escaping.
var tokenAlphabet = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// tokenBytes gives 80 bits of entropy — far past guessable, still short enough
// that the link does not wrap in an email client.
const tokenBytes = 10

// tokenLength is how many characters that encodes to.
const tokenLength = 16

// newToken returns the secret half of an invite link.
func newToken() (string, error) {
	var b [tokenBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("server: generate token: %w", err)
	}
	return tokenAlphabet.EncodeToString(b[:]), nil
}

// hashToken binds the secret to the contact it was issued for.
//
// Both halves go into the digest, so editing the visible half of a link
// produces a hash that matches nothing. That makes the name tamper-evident
// rather than decorative, and it is why the store only ever sees this digest:
// a copy of the database hands over no working links.
func hashToken(name, token string) string {
	sum := sha256.Sum256([]byte(slugify(name) + ":" + token))
	return hex.EncodeToString(sum[:])
}

// slugify turns a contact name into the visible half of a link.
//
// Whatever is typed ends up in a URL the recipient reads, which is the reason
// the note field exists: the name is for them, the note is for you.
func slugify(name string) string {
	var b strings.Builder
	lastDash := true // leading dashes are suppressed
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			// Everything else — spaces, punctuation, accents, em dashes —
			// collapses to a single separator.
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
