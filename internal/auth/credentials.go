// Package auth handles Teleport login for tstui: the credential types
// (this file), the PTY-driven tsh login state machine, its prompt
// patterns, and the certificate expiry watcher.
package auth

import (
	"log/slog"
)

// Secret holds a password or one-time code. It never prints its value:
// String, GoString, Format verbs and slog all show "[REDACTED]". Call
// Wipe as soon as the value has been used.
type Secret struct {
	b []byte
}

// NewSecret takes ownership of b; the caller must not keep using it.
func NewSecret(b []byte) *Secret {
	return &Secret{b: b}
}

// Bytes returns the secret value. Do not convert it to a string, which
// cannot be wiped.
func (s *Secret) Bytes() []byte {
	if s == nil {
		return nil
	}
	return s.b
}

// Empty reports whether no secret is set.
func (s *Secret) Empty() bool { return s == nil || len(s.b) == 0 }

// Wipe overwrites the secret with zeros and releases it.
func (s *Secret) Wipe() {
	if s == nil {
		return
	}
	for i := range s.b {
		s.b[i] = 0
	}
	s.b = nil
}

const redacted = "[REDACTED]"

func (s *Secret) String() string {
	if s.Empty() {
		return ""
	}
	return redacted
}

// GoString keeps %#v from printing the bytes.
func (s *Secret) GoString() string { return s.String() }

// LogValue keeps slog from printing the bytes.
func (s *Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// Credentials are the login inputs gathered from flags, environment and
// config. Missing values are asked for in the login popup.
type Credentials struct {
	Proxy    string
	User     string
	Password *Secret
	OTP      *Secret
	// AuthConnector is passed to tsh --auth.
	AuthConnector string
}

// Wipe wipes every secret in c.
func (c *Credentials) Wipe() {
	c.Password.Wipe()
	c.OTP.Wipe()
}
