package ui

import (
	"testing"

	"go.uber.org/goleak"
)

// Every test stops its app; nothing may outlive it: sessions, PTY
// readers, pump, timers.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
