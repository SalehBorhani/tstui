package auth

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Patterns recognise what `tsh login` writes to its terminal: the prompts
// it waits on, informational lines, and the error text it fails with.
// Each entry is a list so that wordings from several tsh versions can be
// matched side by side. The defaults come from docs/tsh-notes.md and
// can be extended from the config file (tsh_prompts).
type Patterns struct {
	// Prompts maps each prompt kind to the expressions that recognise it.
	Prompts map[PromptKind][]*regexp.Regexp
	// KeyTapped recognises tsh confirming a security key touch.
	KeyTapped []*regexp.Regexp
	// SSOLink recognises the line that announces the browser URL of an SSO
	// login. The first URL on that line or a following one is reported.
	SSOLink []*regexp.Regexp
	// Failures maps each failure kind to the expressions that recognise
	// it in tsh's error text. ErrWrongPassword stands for every "bad
	// credentials" message; Login refines it by what was typed last.
	Failures map[error][]*regexp.Regexp
}

// Default expressions. All are case-insensitive. Prompt expressions are
// matched against one line of output at a time.
//
// Wording seen in tsh v18.11.3 (from the binary; see docs/tsh-notes.md):
//
//	Enter password for Teleport user %v
//	Enter an OTP code from a %sdevice
//	Tap any %ssecurity key
//	Tap any %ssecurity key or enter a code from a %sOTP device
//	Detected security key tap
//	Enter your security key PIN
//	If browser window does not open automatically, open it by clicking on the link:
var defaultPatterns = map[string][]string{
	"password": {
		`enter (your |the )?password\b.*`,
		`^\s*password:\s*$`,
	},
	"otp": {
		`enter (an? |your )?(otp|totp|one[- ]time|2fa|mfa|authenticator)\b.*`,
		`enter .*code from .*device.*`,
	},
	"otp_or_tap": {
		`(tap|touch) .*security key.* (or|and) (enter|type) .*code.*`,
	},
	"tap": {
		`(tap|touch|insert) (any |your )?.*security key.*`,
	},
	"pin": {
		`enter (your )?(security key |yubikey piv |new security key )?pin\b.*`,
	},
	"choice": {
		`^\s*(choose|select|pick) .*`,
	},
	"key_tapped": {
		`detected security key tap`,
		`security key (tap|touch) detected`,
	},
	"sso_link": {
		`open it by clicking on the link`,
		`browser window does not open`,
		`open the following url`,
	},
	"locked": {
		`too many (incorrect|failed|invalid) (login )?attempts`,
		`\b(user|account) .*\bis locked\b`,
		`locked (until|out)`,
		`lock targeting .* is in force`,
	},
	"connector_not_found": {
		`connector .*not found`,
		`not found.*connector`,
		`unknown (auth(entication)? )?connector`,
		`unsupported auth(entication)? type`,
	},
	"wrong_credentials": {
		`invalid (username|user name|password|credentials|otp|totp|token|code|second factor)`,
		`(incorrect|wrong|bad) (password|code|otp|credentials)`,
		`failed to validate`,
		`access denied`,
		`second factor`,
	},
	"tls": {
		`x509:`,
		`\btls:`,
		`certificate (signed by unknown authority|is not valid|has expired or is not yet valid)`,
		`handshake failure`,
	},
	"network": {
		`connection refused`,
		`no such host`,
		`network is unreachable`,
		`no route to host`,
		`i/o timeout`,
		`connection reset`,
		`dial tcp`,
		`context deadline exceeded`,
		`server misbehaving`,
	},
}

// PatternKeys are the names accepted in the config's tsh_prompts map.
func PatternKeys() []string {
	keys := make([]string, 0, len(defaultPatterns))
	for k := range defaultPatterns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// DefaultPatterns returns the built-in patterns.
func DefaultPatterns() *Patterns {
	p, err := NewPatterns(nil)
	if err != nil {
		panic(err) // the defaults are constants; a failure is a programming error
	}
	return p
}

// NewPatterns builds the patterns from the defaults plus extra
// expressions per key (see PatternKeys). Extra expressions are tried
// before the defaults and are matched case-insensitively.
func NewPatterns(extra map[string][]string) (*Patterns, error) {
	p := &Patterns{
		Prompts:  map[PromptKind][]*regexp.Regexp{},
		Failures: map[error][]*regexp.Regexp{},
	}
	for key := range extra {
		if _, ok := defaultPatterns[key]; !ok {
			return nil, fmt.Errorf("tsh_prompts: unknown key %q (known: %s)", key, strings.Join(PatternKeys(), ", "))
		}
	}
	for key, defaults := range defaultPatterns {
		exprs := append(append([]string(nil), extra[key]...), defaults...)
		res := make([]*regexp.Regexp, 0, len(exprs))
		for _, e := range exprs {
			re, err := regexp.Compile("(?im)" + e)
			if err != nil {
				return nil, fmt.Errorf("tsh_prompts.%s: %w", key, err)
			}
			res = append(res, re)
		}
		switch key {
		case "password":
			p.Prompts[PromptPassword] = res
		case "otp":
			p.Prompts[PromptOTP] = res
		case "otp_or_tap":
			p.Prompts[PromptOTPOrTap] = res
		case "tap":
			p.Prompts[PromptTap] = res
		case "pin":
			p.Prompts[PromptPIN] = res
		case "choice":
			p.Prompts[PromptChoice] = res
		case "key_tapped":
			p.KeyTapped = res
		case "sso_link":
			p.SSOLink = res
		case "locked":
			p.Failures[ErrLocked] = res
		case "connector_not_found":
			p.Failures[ErrConnectorNotFound] = res
		case "wrong_credentials":
			p.Failures[ErrWrongPassword] = res
		case "tls":
			p.Failures[ErrTLS] = res
		case "network":
			p.Failures[ErrNetwork] = res
		}
	}
	return p, nil
}

// failureOrder is the order failure kinds are tested in: the more
// specific ones first ("too many incorrect attempts" also reads as
// "incorrect").
var failureOrder = []error{ErrLocked, ErrConnectorNotFound, ErrTLS, ErrWrongPassword, ErrNetwork}

// classify returns the failure kind for tsh's error text, or nil.
func (p *Patterns) classify(msg string) error {
	for _, kind := range failureOrder {
		if matchAny(p.Failures[kind], msg) {
			return kind
		}
	}
	return nil
}

// promptOrder breaks ties between prompts that match the same line: the
// combined "tap or enter a code" prompt also reads as a tap prompt and as
// an OTP prompt.
var promptOrder = []PromptKind{PromptOTPOrTap, PromptPIN, PromptTap, PromptPassword, PromptOTP, PromptChoice}

// matchPrompt returns the prompt kind that line asks for, if any.
func (p *Patterns) matchPrompt(line string) (PromptKind, bool) {
	for _, kind := range promptOrder {
		if matchAny(p.Prompts[kind], line) {
			return kind, true
		}
	}
	return 0, false
}

var urlRE = regexp.MustCompile(`https?://\S+`)

func matchAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
