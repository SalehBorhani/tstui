package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/root-ali/tstui/internal/tsh"
)

// DefaultLoginTimeout bounds a whole login, including the time a person
// needs to type a code or find their security key.
const DefaultLoginTimeout = 3 * time.Minute

// settleDelay is how long a line without a newline must stay unchanged
// before it is taken as a complete prompt. Prompts often end in ": "
// without a newline, and output can arrive split mid-line.
const settleDelay = 150 * time.Millisecond

// drainDelay is how long to keep reading output after tsh has exited.
const drainDelay = 500 * time.Millisecond

// maxTranscriptLines bounds the output kept for error messages.
const maxTranscriptLines = 200

// LoginRequest describes one `tsh login`.
type LoginRequest struct {
	// Proxy is the proxy address (host[:port]); required.
	Proxy string
	// User is the Teleport user; empty lets tsh pick its default.
	User string
	// Password and OTP answer the first password and OTP prompts. Login
	// owns them and wipes them before it returns. Anything else tsh asks
	// for goes to Prompter.
	Password *Secret
	OTP      *Secret
	// AuthConnector is passed as --auth.
	AuthConnector string
	// MFAMode is passed as --mfa-mode (auto, otp, cross-platform, ...).
	MFAMode string
	// Insecure passes --insecure (test clusters only).
	Insecure bool
	// Prompter answers prompts the request cannot. Nil makes such prompts
	// fail the login with ErrInputNeeded.
	Prompter Prompter
}

// Wipe wipes the request's secrets.
func (r *LoginRequest) Wipe() {
	r.Password.Wipe()
	r.OTP.Wipe()
}

func (r *LoginRequest) args() []string {
	args := []string{"login", "--proxy=" + r.Proxy}
	if r.User != "" {
		args = append(args, "--user="+r.User)
	}
	if r.AuthConnector != "" {
		args = append(args, "--auth="+r.AuthConnector)
	}
	if r.MFAMode != "" {
		args = append(args, "--mfa-mode="+r.MFAMode)
	}
	if r.Insecure {
		args = append(args, "--insecure")
	}
	return args
}

// Service logs in to and out of Teleport proxies through tsh.
type Service struct {
	client   tsh.Client
	patterns *Patterns
	logger   *slog.Logger
	timeout  time.Duration
}

// Options configures NewService. Zero values give the defaults.
type Options struct {
	Patterns *Patterns
	Logger   *slog.Logger
	// Timeout bounds each Login; default DefaultLoginTimeout.
	Timeout time.Duration
}

// NewService returns a Service that runs tsh through client.
func NewService(client tsh.Client, o Options) *Service {
	s := &Service{client: client, patterns: o.Patterns, logger: o.Logger, timeout: o.Timeout}
	if s.patterns == nil {
		s.patterns = DefaultPatterns()
	}
	if s.logger == nil {
		s.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if s.timeout <= 0 {
		s.timeout = DefaultLoginTimeout
	}
	return s
}

// Logout runs `tsh logout` for proxy (and user, if given).
func (s *Service) Logout(ctx context.Context, proxy, user string) error {
	if err := s.client.Logout(ctx, proxy, user); err != nil {
		return fmt.Errorf("log out of %s: %w", proxy, err)
	}
	s.logger.Info("logged out", "proxy", proxy, "user", user)
	return nil
}

// Login runs `tsh login` in a pseudo-terminal and answers its prompts:
// from req first, then through req.Prompter. It returns nil once tsh
// has logged in. Failures wrap one of the Err* kinds of this package
// with tsh's message. The secrets in req are wiped before Login returns.
//
// Login blocks; call it from a goroutine, never the UI thread.
func (s *Service) Login(ctx context.Context, req LoginRequest) error {
	defer req.Wipe()
	if req.Proxy == "" {
		return fmt.Errorf("%w: no proxy given", ErrLoginFailed)
	}

	ctx, cancel := context.WithTimeoutCause(ctx, s.timeout, ErrTimeout)
	defer cancel()

	start := time.Now()
	proc, err := s.client.StartPTY(ctx, req.args(), tsh.PTYOptions{NoEcho: true, Rows: 40, Cols: 250})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLoginFailed, err)
	}
	defer proc.Close()

	m := &machine{
		req:      &req,
		proc:     proc,
		patterns: s.patterns,
		logger:   s.logger.With("proxy", req.Proxy, "user", req.User),
		asked:    map[PromptKind]int{},
		answers:  make(chan answer),
	}
	err = m.run(ctx)
	m.logger.Info("login finished", "ok", err == nil, "result", resultKind(err), "duration", time.Since(start).Round(time.Millisecond))
	return err
}

// resultKind names the failure kind of a Login error for logs, without
// tsh's message.
func resultKind(err error) string {
	if err == nil {
		return "ok"
	}
	for _, kind := range []error{ErrWrongPassword, ErrWrongOTP, ErrBadCredentials, ErrLocked, ErrConnectorNotFound,
		ErrTLS, ErrNetwork, ErrTimeout, ErrCancelled, ErrInputNeeded, ErrLoginFailed} {
		if errors.Is(err, kind) {
			return kind.Error()
		}
	}
	return "error"
}

// answer is the result of one Prompter.Ask.
type answer struct {
	seq    int
	kind   PromptKind
	secret *Secret
	err    error
}

// machine is the expect-style state machine that drives one tsh login.
type machine struct {
	req      *LoginRequest
	proc     tsh.Process
	patterns *Patterns
	logger   *slog.Logger

	raw        []byte   // output not yet split into lines
	handled    int      // bytes of the unterminated line in raw already handled
	transcript []string // cleaned output lines, newest last
	queryTail  []byte   // end of the previous chunk, for queries split across reads

	asked    map[PromptKind]int // how often each prompt was seen
	lastSent PromptKind         // the last secret typed into tsh
	ssoArmed bool               // an SSO link announcement was seen

	// The outstanding Prompter.Ask, if any.
	askSeq    int
	askCancel context.CancelFunc
	answers   chan answer
}

func (m *machine) run(ctx context.Context) (err error) {
	defer func() {
		m.cancelAsk()
		// The transcript has no secrets (echo is off), but wipe it anyway.
		for i := range m.transcript {
			m.transcript[i] = ""
		}
		wipe(m.raw)
		wipe(m.queryTail)
	}()

	chunks := make(chan []byte, 16)
	stop := make(chan struct{})
	defer close(stop)
	go readLoop(m.proc, chunks, stop)

	settle := time.NewTimer(time.Hour)
	settle.Stop()
	defer settle.Stop()
	var drain <-chan time.Time
	exited := m.proc.Done()

	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				if exited == nil { // tsh exited and all output is read
					return m.finish(ctx)
				}
				continue
			}
			if err := m.answerQueries(chunk); err != nil {
				return err
			}
			m.raw = append(m.raw, chunk...)
			wipe(chunk)
			if err := m.processLines(ctx, false); err != nil {
				return err
			}
			if len(m.raw) > m.handled {
				settle.Reset(settleDelay)
			}

		case <-settle.C:
			if err := m.processLines(ctx, true); err != nil {
				return err
			}

		case a := <-m.answers:
			if err := m.handleAnswer(a); err != nil {
				return err
			}

		case <-exited:
			exited = nil
			if chunks == nil {
				return m.finish(ctx)
			}
			drain = time.After(drainDelay)

		case <-drain:
			return m.finish(ctx)

		case <-ctx.Done():
			_ = m.proc.Kill()
			return m.contextError(ctx)
		}
	}
}

// contextError reports why ctx ended: the login timeout, or cancellation
// by the caller.
func (m *machine) contextError(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), ErrTimeout) {
		m.logger.Warn("login timed out", "waiting_for", m.waitingFor())
		return fmt.Errorf("%w (%s)", ErrTimeout, m.waitingFor())
	}
	return fmt.Errorf("%w: %w", ErrCancelled, ctx.Err())
}

func readLoop(r io.Reader, chunks chan<- []byte, stop <-chan struct{}) {
	defer close(chunks)
	buf := make([]byte, 4096)
	defer wipe(buf)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			select {
			case chunks <- append([]byte(nil), buf[:n]...):
			case <-stop:
				return
			}
		}
		if err != nil {
			return // io.EOF, or EIO once tsh has exited and closed the terminal
		}
	}
}

func (m *machine) waitingFor() string {
	if m.askCancel != nil {
		return "waiting for an answer"
	}
	if len(m.transcript) == 0 {
		return "no output from tsh"
	}
	return fmt.Sprintf("last output: %q", truncate(m.transcript[len(m.transcript)-1], 120))
}

// processLines handles every complete line in raw, and the trailing
// partial line too when partial is set (it has been quiet for settleDelay).
// A partial line is handled once; when more of the same line arrives,
// only the new part is handled. tsh v18 writes terminal queries without a
// newline and then waits before printing the password prompt on the same
// line, so the new part must not be dropped.
func (m *machine) processLines(ctx context.Context, partial bool) error {
	for {
		i := bytes.IndexByte(m.raw, '\n')
		if i < 0 {
			break
		}
		start := min(m.handled, i)
		line := cleanLine(m.raw[start:i])
		m.handled = 0
		wipe(m.raw[:i+1])
		m.raw = m.raw[i+1:]
		if err := m.handleLine(ctx, line); err != nil {
			return err
		}
	}
	if !partial || len(m.raw) <= m.handled {
		return nil
	}
	end := len(m.raw)
	if loc := partialEscapeRE.FindIndex(m.raw[m.handled:]); loc != nil {
		end = m.handled + loc[0] // keep a half-received escape for later
	}
	if end <= m.handled {
		return nil
	}
	line := cleanLine(m.raw[m.handled:end])
	m.handled = end
	return m.handleLine(ctx, line)
}

// Terminal queries that tsh (through its colour detection) sends before
// the first prompt, with the replies a dark terminal would give. Without
// a reply tsh waits about 5 seconds (verified with v18.11.3).
var terminalQueries = []struct{ query, reply []byte }{
	{[]byte("\x1b]11;?"), []byte("\x1b]11;rgb:0000/0000/0000\x1b\\")}, // background colour
	{[]byte("\x1b]10;?"), []byte("\x1b]10;rgb:ffff/ffff/ffff\x1b\\")}, // foreground colour
	{[]byte("\x1b[6n"), []byte("\x1b[1;1R")},                          // cursor position
	{[]byte("\x1b[c"), []byte("\x1b[?62;22c")},                        // device attributes
}

const maxQueryLen = 8

// answerQueries replies to terminal queries in chunk, like a terminal.
func (m *machine) answerQueries(chunk []byte) error {
	buf := make([]byte, 0, len(m.queryTail)+len(chunk))
	buf = append(append(buf, m.queryTail...), chunk...)
	for {
		first, idx := -1, len(buf)
		for i, q := range terminalQueries {
			if j := bytes.Index(buf, q.query); j >= 0 && j < idx {
				first, idx = i, j
			}
		}
		if first < 0 {
			break
		}
		q := terminalQueries[first]
		if _, err := m.proc.Write(q.reply); err != nil {
			return fmt.Errorf("%w: answer terminal query: %w", ErrLoginFailed, err)
		}
		m.logger.Debug("answered terminal query", "query", fmt.Sprintf("%q", q.query))
		buf = buf[idx+len(q.query):]
	}
	if len(buf) > maxQueryLen-1 {
		buf = buf[len(buf)-(maxQueryLen-1):]
	}
	m.queryTail = append([]byte(nil), buf...)
	return nil
}

var (
	// CSI sequences, OSC strings (ended by BEL or ST), charset and keypad
	// selections.
	ansiRE = regexp.MustCompile(`\x1b\[[0-9;?<>=]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>]`)
	// An escape sequence cut off at the end of the data received so far.
	partialEscapeRE = regexp.MustCompile(`\x1b(?:\[[0-9;?<>=]*[ -/]*|\][^\x07\x1b]*|[()])?$`)
)

// cleanLine removes colours and carriage returns.
func cleanLine(b []byte) string {
	s := ansiRE.ReplaceAllString(string(b), "")
	s = partialEscapeRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.TrimRight(s, " \t")
}

func (m *machine) handleLine(ctx context.Context, line string) error {
	if strings.TrimSpace(line) == "" {
		return nil
	}
	m.transcript = append(m.transcript, line)
	if len(m.transcript) > maxTranscriptLines {
		m.transcript = m.transcript[len(m.transcript)-maxTranscriptLines:]
	}

	if m.ssoArmed {
		if u := urlRE.FindString(line); u != "" {
			m.ssoArmed = false
			m.notify(Event{Kind: EventSSOURL, Text: line, URL: u})
			return nil
		}
	}
	if matchAny(m.patterns.SSOLink, line) {
		if u := urlRE.FindString(line); u != "" {
			m.notify(Event{Kind: EventSSOURL, Text: line, URL: u})
		} else {
			m.ssoArmed = true
		}
		return nil
	}
	if matchAny(m.patterns.KeyTapped, line) {
		m.cancelAsk() // an OTP-or-tap question is moot now
		m.notify(Event{Kind: EventKeyTapped, Text: line})
		return nil
	}
	kind, ok := m.patterns.matchPrompt(line)
	if !ok {
		return nil
	}
	return m.handlePrompt(ctx, kind, line)
}

func (m *machine) handlePrompt(ctx context.Context, kind PromptKind, line string) error {
	// A new prompt means tsh has moved on; any open question is stale.
	m.cancelAsk()
	m.asked[kind]++
	m.logger.Debug("login prompt", "kind", kind.String(), "count", m.asked[kind])

	switch kind {
	case PromptPassword:
		if m.asked[kind] == 1 && !m.req.Password.Empty() {
			return m.send(kind, m.req.Password, "request")
		}
	case PromptOTP, PromptOTPOrTap:
		if m.asked[PromptOTP]+m.asked[PromptOTPOrTap] == 1 && !m.req.OTP.Empty() {
			return m.send(kind, m.req.OTP, "request")
		}
		if kind == PromptOTPOrTap {
			m.notify(Event{Kind: EventTapKey, Text: line})
		}
	case PromptTap:
		m.notify(Event{Kind: EventTapKey, Text: line})
		return nil
	}

	p := Prompt{Kind: kind, Text: strings.TrimSpace(line), Retry: m.asked[kind] > 1}
	if kind == PromptChoice {
		p.Options = m.choiceOptions(line)
	}
	if m.req.Prompter == nil {
		_ = m.proc.Kill()
		return fmt.Errorf("%w: %s", ErrInputNeeded, p.Text)
	}
	m.ask(ctx, p)
	return nil
}

// ask runs Prompter.Ask in its own goroutine, so tsh's output keeps being
// read while the user thinks; a key touch can make the question moot.
func (m *machine) ask(ctx context.Context, p Prompt) {
	askCtx, cancel := context.WithCancel(ctx)
	m.askSeq++
	m.askCancel = cancel
	seq := m.askSeq
	prompter := m.req.Prompter
	answers := m.answers
	go func() {
		s, err := prompter.Ask(askCtx, p)
		select {
		case answers <- answer{seq: seq, kind: p.Kind, secret: s, err: err}:
		case <-askCtx.Done():
			s.Wipe()
		}
	}()
}

func (m *machine) cancelAsk() {
	if m.askCancel != nil {
		m.askCancel()
		m.askCancel = nil
	}
}

func (m *machine) handleAnswer(a answer) error {
	if a.seq != m.askSeq || m.askCancel == nil {
		a.secret.Wipe() // answer to a question that is no longer open
		return nil
	}
	m.cancelAsk()
	if a.err != nil {
		a.secret.Wipe()
		_ = m.proc.Kill()
		if errors.Is(a.err, ErrCancelled) || errors.Is(a.err, context.Canceled) {
			return ErrCancelled
		}
		return fmt.Errorf("%w: %w", ErrCancelled, a.err)
	}
	return m.send(a.kind, a.secret, "prompter")
}

// send types s into tsh followed by Enter, then wipes s.
func (m *machine) send(kind PromptKind, s *Secret, source string) error {
	defer s.Wipe()
	// An empty answer is typed too: tsh reports the problem itself.
	if _, err := m.proc.Write(s.Bytes()); err != nil {
		return fmt.Errorf("%w: type into tsh: %w", ErrLoginFailed, err)
	}
	// "\n", not "\r": in raw mode the tty does not turn \r into \n, and
	// golang.org/x/term (which tsh uses for passwords) ignores \r on Unix.
	if _, err := m.proc.Write([]byte("\n")); err != nil {
		return fmt.Errorf("%w: type into tsh: %w", ErrLoginFailed, err)
	}
	if kind == PromptPassword || kind == PromptOTP || kind == PromptOTPOrTap {
		m.lastSent = kind
	}
	m.logger.Debug("login answer typed", "kind", kind.String(), "source", source)
	m.notify(Event{Kind: EventAnswered, Text: kind.String()})
	return nil
}

func (m *machine) notify(e Event) {
	m.logger.Debug("login event", "kind", e.Kind.String())
	if m.req.Prompter != nil {
		m.req.Prompter.Notify(e)
	}
}

var (
	numberedOptionRE = regexp.MustCompile(`^\s*\[?(\d+)[\]).:]\s+(.+)$`)
	bracketOptionsRE = regexp.MustCompile(`\[([^\]]+,[^\]]+)\]`)
)

// choiceOptions finds the options of a choice prompt: "[a, b]" on the
// prompt line, or numbered lines printed just before it.
func (m *machine) choiceOptions(line string) []string {
	if mm := bracketOptionsRE.FindStringSubmatch(line); mm != nil {
		var opts []string
		for _, o := range strings.Split(mm[1], ",") {
			if o = strings.TrimSpace(o); o != "" {
				opts = append(opts, o)
			}
		}
		return opts
	}
	var opts []string
	for i := len(m.transcript) - 2; i >= 0; i-- {
		mm := numberedOptionRE.FindStringSubmatch(m.transcript[i])
		if mm == nil {
			break
		}
		opts = append([]string{mm[1] + ": " + strings.TrimSpace(mm[2])}, opts...)
	}
	return opts
}

// finish turns tsh's exit status and last output into Login's result.
func (m *machine) finish(ctx context.Context) error {
	if ctx.Err() != nil {
		// tsh exited because it was killed for the timeout or a cancel.
		return m.contextError(ctx)
	}
	code := m.proc.ExitCode()
	if code == 0 {
		return nil
	}
	msg := m.errorMessage()
	kind := m.patterns.classify(msg)
	switch {
	case kind == nil:
		kind = ErrLoginFailed
	case errors.Is(kind, ErrWrongPassword):
		// tsh (v18 at least) checks the password before it asks for the
		// second factor, so the last thing typed is the one that was wrong.
		switch m.lastSent {
		case PromptPassword:
			kind = ErrWrongPassword
		case PromptOTP, PromptOTPOrTap:
			kind = ErrWrongOTP
		default:
			kind = ErrBadCredentials
		}
	}
	if msg == "" {
		msg = fmt.Sprintf("tsh login exited with status %d", code)
	}
	return fmt.Errorf("%w: %s", kind, msg)
}

// errorMessage returns tsh's error text: the "ERROR:" lines if any,
// else the last line of output.
func (m *machine) errorMessage() string {
	var errLines []string
	for i, l := range m.transcript {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "ERROR:"); ok {
			msg := strings.TrimSpace(rest)
			// tsh exits right after its error; the lines after it are
			// part of the message (hints, wrapped text).
			for _, next := range m.transcript[i+1:] {
				if strings.HasPrefix(strings.TrimSpace(next), "ERROR:") {
					break
				}
				msg += " " + strings.TrimSpace(next)
			}
			errLines = append(errLines, msg)
		}
	}
	if len(errLines) > 0 {
		return truncate(strings.Join(errLines, "; "), 400)
	}
	if n := len(m.transcript); n > 0 {
		return truncate(strings.TrimSpace(m.transcript[n-1]), 400)
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
