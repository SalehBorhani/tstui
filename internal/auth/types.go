package auth

import (
	"context"
	"errors"
)

// PromptKind is something tsh login waits for.
type PromptKind int

// Prompt kinds.
const (
	// PromptPassword asks for the Teleport password.
	PromptPassword PromptKind = iota + 1
	// PromptOTP asks for a one-time code from an authenticator app.
	PromptOTP
	// PromptOTPOrTap accepts either a security key touch or an OTP code.
	// The question is withdrawn (its context cancelled) when the key is
	// touched.
	PromptOTPOrTap
	// PromptTap waits for a security key touch. It is reported with
	// Notify(EventTapKey), never asked.
	PromptTap
	// PromptPIN asks for the security key PIN.
	PromptPIN
	// PromptChoice asks to pick one of Prompt.Options; the answer is typed
	// as given.
	PromptChoice
)

func (k PromptKind) String() string {
	switch k {
	case PromptPassword:
		return "password"
	case PromptOTP:
		return "otp"
	case PromptOTPOrTap:
		return "otp-or-tap"
	case PromptTap:
		return "tap"
	case PromptPIN:
		return "pin"
	case PromptChoice:
		return "choice"
	}
	return "unknown"
}

// Prompt is a question from tsh that the LoginRequest could not answer.
type Prompt struct {
	Kind PromptKind
	// Text is tsh's prompt line without colours.
	Text string
	// Options are the choices of a PromptChoice, when tsh listed them.
	Options []string
	// Retry is true when tsh asks for the same thing again, which means
	// the previous answer was rejected.
	Retry bool
}

// EventKind is a login state change that needs no answer.
type EventKind int

// Event kinds.
const (
	// EventTapKey: touch your security key now.
	EventTapKey EventKind = iota + 1
	// EventKeyTapped: tsh saw the key touch.
	EventKeyTapped
	// EventSSOURL: an SSO login waits in the browser at Event.URL.
	EventSSOURL
	// EventAnswered: an answer was typed into tsh; login continues.
	EventAnswered
)

func (k EventKind) String() string {
	switch k {
	case EventTapKey:
		return "tap-key"
	case EventKeyTapped:
		return "key-tapped"
	case EventSSOURL:
		return "sso-url"
	case EventAnswered:
		return "answered"
	}
	return "unknown"
}

// Event reports progress of a login.
type Event struct {
	Kind EventKind
	// Text is tsh's line that caused the event, without colours.
	Text string
	// URL is set for EventSSOURL.
	URL string
}

// Prompter answers what tsh asks during a login when the LoginRequest
// does not have it. The UI implements it with the login modal; tests
// implement it with PrompterFuncs.
//
// Ask and Notify are called from the login goroutine, never the UI
// thread, and Ask may be called again (with a new question) while an
// earlier Ask's context is being cancelled.
type Prompter interface {
	// Ask blocks until the user answers or ctx is done. Login takes
	// ownership of the returned Secret and wipes it after typing it.
	// Returning an error (ErrCancelled, typically) aborts the login.
	Ask(ctx context.Context, p Prompt) (*Secret, error)
	// Notify reports an event. It must not block.
	Notify(e Event)
}

// PrompterFuncs adapts plain functions to Prompter. A nil AskFunc fails
// every question with ErrInputNeeded; a nil NotifyFunc drops events.
type PrompterFuncs struct {
	AskFunc    func(ctx context.Context, p Prompt) (*Secret, error)
	NotifyFunc func(e Event)
}

// Ask implements Prompter.
func (f PrompterFuncs) Ask(ctx context.Context, p Prompt) (*Secret, error) {
	if f.AskFunc == nil {
		return nil, ErrInputNeeded
	}
	return f.AskFunc(ctx, p)
}

// Notify implements Prompter.
func (f PrompterFuncs) Notify(e Event) {
	if f.NotifyFunc != nil {
		f.NotifyFunc(e)
	}
}

// Login failures. Login wraps them with tsh's own message, so test with
// errors.Is and show err.Error() to the user.
var (
	// ErrWrongPassword: tsh rejected the credentials right after the
	// password was typed (wrong user name or password).
	ErrWrongPassword = errors.New("wrong user name or password")
	// ErrWrongOTP: tsh rejected the credentials right after the OTP code
	// was typed.
	ErrWrongOTP = errors.New("wrong OTP code")
	// ErrBadCredentials: tsh rejected the credentials and it is unclear
	// which one was wrong.
	ErrBadCredentials = errors.New("wrong credentials")
	// ErrLocked: the user is locked, usually after too many failed attempts.
	ErrLocked = errors.New("user is locked")
	// ErrConnectorNotFound: the --auth connector does not exist.
	ErrConnectorNotFound = errors.New("auth connector not found")
	// ErrTLS: the proxy's certificate could not be verified.
	ErrTLS = errors.New("TLS error")
	// ErrNetwork: the proxy could not be reached.
	ErrNetwork = errors.New("cannot reach the proxy")
	// ErrTimeout: the login did not finish in time.
	ErrTimeout = errors.New("login timed out")
	// ErrCancelled: the user or the caller cancelled the login.
	ErrCancelled = errors.New("login cancelled")
	// ErrInputNeeded: tsh asked for something and there was no Prompter
	// to ask.
	ErrInputNeeded = errors.New("tsh asked for input that was not given")
	// ErrLoginFailed: tsh failed for a reason not recognised above.
	ErrLoginFailed = errors.New("login failed")
)
