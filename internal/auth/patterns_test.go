package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestMatchPrompt(t *testing.T) {
	p := DefaultPatterns()
	tests := []struct {
		line string
		want PromptKind // 0: no prompt
	}{
		// tsh v18.11.3 wording.
		{"Enter password for Teleport user ali:", PromptPassword},
		{"Enter an OTP code from a device:", PromptOTP},
		{"Enter an OTP code from a *registered* device:", PromptOTP},
		{"Tap any security key", PromptTap},
		{"Tap any *registered* security key", PromptTap},
		{"Tap any security key or enter a code from a OTP device", PromptOTPOrTap},
		{"Tap your security key to complete login", PromptTap},
		{"Enter your security key PIN:", PromptPIN},
		{"Choose the user for login [1]: ", PromptChoice},
		// Other wordings seen in older versions or docs.
		{"Password:", PromptPassword},
		{"Enter your OTP token:", PromptOTP},
		{"Enter an OTP code from your authenticator app:", PromptOTP},
		// Not prompts.
		{"Detected security key tap", 0},
		{"> Profile URL:        https://tp.afr.example:443", 0},
		{"  Logged in as:       ali", 0},
		{"ERROR: invalid username or password", 0},
	}
	for _, tt := range tests {
		got, ok := p.matchPrompt(tt.line)
		if !ok {
			got = 0
		}
		if got != tt.want {
			t.Errorf("%q: got %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestClassify(t *testing.T) {
	p := DefaultPatterns()
	tests := []struct {
		msg  string
		want error
	}{
		{"invalid username or password", ErrWrongPassword},
		{"invalid username, password or second factor", ErrWrongPassword},
		{"too many incorrect attempts, please try again later", ErrLocked},
		{`lock targeting User:"ali" is in force: offboarding`, ErrLocked},
		{`connector "okta" not found`, ErrConnectorNotFound},
		{`Get "https://x/webapi/ping": tls: failed to verify certificate: x509: certificate signed by unknown authority`, ErrTLS},
		{"dial tcp 10.0.0.1:443: connect: connection refused", ErrNetwork},
		{"lookup tp.afr.example on 127.0.0.53:53: no such host", ErrNetwork},
		{"something else entirely", nil},
	}
	for _, tt := range tests {
		if got := p.classify(tt.msg); !errors.Is(got, tt.want) || (tt.want == nil && got != nil) {
			t.Errorf("%q: got %v, want %v", tt.msg, got, tt.want)
		}
	}
}

func TestNewPatternsExtra(t *testing.T) {
	p, err := NewPatterns(map[string][]string{"password": {`^Passwort für .*:`}, "locked": {`gesperrt`}})
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := p.matchPrompt("Passwort für ali:"); !ok || k != PromptPassword {
		t.Errorf("extra password pattern not used: %v %v", k, ok)
	}
	if k, ok := p.matchPrompt("Enter password for Teleport user ali:"); !ok || k != PromptPassword {
		t.Error("defaults lost when adding extra patterns")
	}
	if !errors.Is(p.classify("Benutzer gesperrt"), ErrLocked) {
		t.Error("extra failure pattern not used")
	}

	for _, bad := range []map[string][]string{
		{"pasword": {"x"}},
		{"otp": {"("}},
	} {
		if _, err := NewPatterns(bad); err == nil {
			t.Errorf("NewPatterns(%v) accepted bad input", bad)
		} else if !strings.Contains(err.Error(), "tsh_prompts") {
			t.Errorf("error lacks context: %v", err)
		}
	}
}

func TestCleanLine(t *testing.T) {
	tests := map[string]string{
		"\x1b[31mERROR: \x1b[0mnot logged in\r": "ERROR: not logged in",
		"Enter password:  ":                     "Enter password:",
		"half an escape \x1b[3":                 "half an escape",
		"\x1b]11;?\x1b\\\x1b[6nEnter password:": "Enter password:",
		"osc with bel \x1b]0;title\x07done":     "osc with bel done",
	}
	for in, want := range tests {
		if got := cleanLine([]byte(in)); got != want {
			t.Errorf("cleanLine(%q) = %q, want %q", in, got, want)
		}
	}
}
