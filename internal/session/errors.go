package session

import (
	"errors"
	"strings"

	"github.com/root-ali/tstui/internal/tsh"
)

// Kinds of session failure that tstui explains to the user. Test with
// errors.Is.
var (
	ErrAccessDenied = errors.New("access denied: your roles do not allow this login on this server")
	ErrCertExpired  = errors.New("your Teleport certificate has expired: log in again")
	ErrMFARequired  = errors.New("this server requires MFA for every session: touch your security key or enter an OTP when asked")
	ErrUnreachable  = errors.New("the server cannot be reached")
	ErrNotFound     = errors.New("no such server on this proxy")
	ErrFailed       = errors.New("the session failed")
)

// Error is a session failure: a friendly Kind plus what tsh said.
type Error struct {
	Kind error
	// Detail is tsh's own message without the ERROR: prefix and colours.
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Kind.Error()
	}
	return e.Kind.Error() + " (" + e.Detail + ")"
}

func (e *Error) Unwrap() error { return e.Kind }

// classifyRules map lower-case substrings of tsh's message to a kind, in
// order. The texts come from Teleport's source and have not all been seen
// from a real tsh (docs/tsh-notes.md).
var classifyRules = []struct {
	kind  error
	needs []string
}{
	{ErrCertExpired, []string{"cert has expired", "certificate has expired", "profile expired", "not logged in", "certificate expired"}},
	{ErrMFARequired, []string{"mfa", "second factor", "multi-factor", "webauthn"}},
	{ErrAccessDenied, []string{"access denied", "permission denied", "not authorized", "unable to authenticate"}},
	{ErrUnreachable, []string{
		"connection refused", "no route to host", "i/o timeout", "connection reset", "host is unreachable",
		"network is unreachable", "failed to dial", "dial tcp", "no such host", "timed out", "proxy not available",
		"connection closed by",
	}},
	{ErrNotFound, []string{"not found", "no matching", "unknown host", "does not exist"}},
}

// Classify turns what tsh wrote to stderr into an *Error, or returns nil
// when it holds no tsh error. Only lines starting with "ERROR:" count, so a
// remote program's output that happens to mention "denied" is not taken
// for a tsh failure; the last such line wins.
func Classify(stderr []byte) error {
	var msg string
	for _, line := range strings.Split(tsh.CleanOutput(stderr), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "ERROR:"); ok {
			msg = strings.TrimSpace(rest)
		}
	}
	if msg == "" {
		return nil
	}
	lower := strings.ToLower(msg)
	for _, r := range classifyRules {
		for _, n := range r.needs {
			if strings.Contains(lower, n) {
				return &Error{Kind: r.kind, Detail: msg}
			}
		}
	}
	return &Error{Kind: ErrFailed, Detail: msg}
}
