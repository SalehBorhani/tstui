package auth

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretNeverPrints(t *testing.T) {
	c := Credentials{User: "ali", Password: NewSecret([]byte("hunter2")), OTP: NewSecret([]byte("123456"))}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	logger.Info("login", "password", c.Password, "otp", c.OTP)
	logger.Info("creds", "c", c)

	outputs := []string{
		fmt.Sprint(c.Password),
		fmt.Sprintf("%v %s %+v %#v", c, c, c, c),
		fmt.Sprintf("%v", &c),
		logBuf.String(),
	}
	for _, out := range outputs {
		if strings.Contains(out, "hunter2") || strings.Contains(out, "123456") {
			t.Errorf("secret leaked: %s", out)
		}
	}
	if !strings.Contains(logBuf.String(), "password=[REDACTED]") {
		t.Errorf("log should show the secret as redacted: %s", logBuf.String())
	}
}

func TestSecretWipe(t *testing.T) {
	buf := []byte("hunter2")
	s := NewSecret(buf)
	if string(s.Bytes()) != "hunter2" || s.Empty() {
		t.Fatal("secret not stored")
	}
	s.Wipe()
	if !bytes.Equal(buf, make([]byte, 7)) {
		t.Fatalf("backing array not zeroed: %q", buf)
	}
	if !s.Empty() || s.Bytes() != nil || s.String() != "" {
		t.Fatal("wiped secret still has a value")
	}

	var nilSecret *Secret
	nilSecret.Wipe() // must not panic
	if !nilSecret.Empty() {
		t.Fatal("nil secret not empty")
	}

	c := Credentials{Password: NewSecret([]byte("a")), OTP: nil}
	c.Wipe()
	if !c.Password.Empty() {
		t.Fatal("credentials not wiped")
	}
}
