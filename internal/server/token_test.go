package server

import (
	"strings"
	"testing"
)

// The email is bound into the digest, so editing the visible address in a link
// yields a hash that matches nothing. That is what makes it tamper-evident
// rather than decorative.
func TestHashTokenBindsEmail(t *testing.T) {
	const token = "abc234xyz"
	base := hashToken("dana@example.com", token)

	if got := hashToken("eve@example.com", token); got == base {
		t.Error("same token under a different email produced the same hash")
	}
	if got := hashToken("dana@example.com", "other"); got == base {
		t.Error("different token produced the same hash")
	}
	// Case and padding are normalized, so a link still works when an email
	// client rewrites the address.
	if got := hashToken("  Dana@Example.COM ", token); got != base {
		t.Error("hash is sensitive to case or surrounding space")
	}
	if len(base) != 64 {
		t.Errorf("digest is %d chars, want 64", len(base))
	}
}

func TestNewTokenIsUnique(t *testing.T) {
	seen := make(map[string]bool, 200)
	for range 200 {
		tok, err := newToken()
		if err != nil {
			t.Fatalf("newToken: %v", err)
		}
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
	}
}

// The visible half of a link comes from the contact name, so whatever is typed
// there is read by whoever receives it. That is the reason the note is a
// separate field.
func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Acme Corp", "acme-corp"},
		{"  Acme   Corp  ", "acme-corp"},
		{"dana@example.com", "dana-example-com"},
		{"Acme — Staff Eng!", "acme-staff-eng"},
		{"Ünïcôde Ltd", "n-c-de-ltd"},
		{"!!!", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A session id is only ever a grouping key, so anything not shaped like one we
// issued is discarded rather than written into a row.
func TestValidSessionID(t *testing.T) {
	good, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	if !validSessionID(good) {
		t.Errorf("rejected an id we issued: %q", good)
	}
	for _, bad := range []string{
		"", "short", "UPPERCASE1234567", "abcdefgh12345678",
		"../../etc/passwd", strings.Repeat("a", 64),
	} {
		if validSessionID(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
