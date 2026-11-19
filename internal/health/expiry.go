package health

import "time"

// Certificate expiry thresholds. The header colours and the toasts both
// use them.
const (
	ExpiryWarn     = 30 * time.Minute
	ExpiryCritical = 5 * time.Minute
)

// ExpiryLevel is how close a certificate is to expiring.
type ExpiryLevel int

// Expiry levels, in increasing urgency.
const (
	ExpiryOK ExpiryLevel = iota
	// ExpirySoon: under ExpiryWarn left.
	ExpirySoon
	// ExpiryImminent: under ExpiryCritical left.
	ExpiryImminent
	// ExpiryPast: expired.
	ExpiryPast
)

// LevelFor classifies the time left on a certificate.
func LevelFor(left time.Duration) ExpiryLevel {
	switch {
	case left <= 0:
		return ExpiryPast
	case left < ExpiryCritical:
		return ExpiryImminent
	case left < ExpiryWarn:
		return ExpirySoon
	}
	return ExpiryOK
}

// ExpiryAlerts decides when to show an expiry notification: once when the
// certificate drops under 30 minutes, once under 5, once when it expires.
// A renewed certificate starts over. Levels jumped over are not shown:
// starting at 4 minutes left shows only the 5-minute alert.
//
// The zero value is ready to use. It is not safe for concurrent use.
type ExpiryAlerts struct {
	fired ExpiryLevel
}

// Update is called with the certificate's expiry (zero: not logged in)
// and the current time. It returns the level to announce, or ExpiryOK when
// there is nothing new to say.
func (e *ExpiryAlerts) Update(validUntil, now time.Time) ExpiryLevel {
	if validUntil.IsZero() {
		e.fired = ExpiryOK
		return ExpiryOK
	}
	lvl := LevelFor(validUntil.Sub(now))
	if lvl <= e.fired {
		e.fired = lvl // renewed: later thresholds fire again
		return ExpiryOK
	}
	e.fired = lvl
	return lvl
}
