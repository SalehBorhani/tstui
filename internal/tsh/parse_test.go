package tsh

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fake-tsh", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCleanOutput(t *testing.T) {
	got := CleanOutput([]byte("\x1b[31mERROR: \x1b[0mActive profile expired.\n\n"))
	if want := "ERROR: Active profile expired."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		stderr  string
		wantIs  error
		wantMsg string
	}{
		{"\x1b[31mERROR: \x1b[0mNot logged in.\n", ErrNotLoggedIn, "Not logged in."},
		{"\x1b[31mERROR: \x1b[0mActive profile expired.\n", ErrNotLoggedIn, "Active profile expired."},
		{"ERROR: ssh: cert has expired\n", ErrNotLoggedIn, "ssh: cert has expired"},
		{"ERROR: access denied to perform action \"list\" on \"node\"", ErrAccessDenied, "access denied"},
		{"ERROR: dial tcp: lookup x: no such host", nil, "no such host"},
		{"", nil, "without an error message"},
	}
	for _, tt := range tests {
		err := classifyError([]byte(tt.stderr))
		if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
			t.Errorf("classifyError(%q) = %v, want errors.Is %v", tt.stderr, err, tt.wantIs)
		}
		if tt.wantIs == nil && (errors.Is(err, ErrNotLoggedIn) || errors.Is(err, ErrAccessDenied)) {
			t.Errorf("classifyError(%q) = %v, want an unclassified error", tt.stderr, err)
		}
		if !strings.Contains(err.Error(), tt.wantMsg) {
			t.Errorf("classifyError(%q) = %q, want it to contain %q", tt.stderr, err, tt.wantMsg)
		}
		if strings.Contains(err.Error(), "\x1b") {
			t.Errorf("classifyError(%q) kept ANSI escapes", tt.stderr)
		}
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		want    string
		wantErr bool
	}{
		{"client only", fixture(t, "version-client.json"), "18.11.3", false},
		{"with proxy", fixture(t, "version.json"), "18.11.3", false},
		{"no version", []byte(`{}`), "", true},
		{"garbage", []byte(`Teleport v18`), "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := parseVersion(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if v.Version != tt.want {
				t.Fatalf("version = %q, want %q", v.Version, tt.want)
			}
		})
	}
}

func TestParseStatus(t *testing.T) {
	valid := strings.Replace(string(fixture(t, "status.json")), "__VALID_UNTIL__", "2026-11-17T22:14:28+03:30", 1)
	tests := []struct {
		name    string
		in      string
		want    Status
		wantErr bool
	}{
		{"empty means no profile", "", Status{}, false},
		{"null active", `{"active":null,"profiles":[]}`, Status{}, false},
		{"profile", valid, Status{
			LoggedIn:   true,
			ProfileURL: "https://tp.afr.example:443",
			User:       "ali",
			Cluster:    "afr-prod",
			Roles:      []string{"access", "editor"},
			Logins:     []string{"ali", "root"},
			ValidUntil: time.Date(2026, 11, 17, 18, 44, 28, 0, time.UTC),
			Extensions: []string{"login-ip", "permit-agent-forwarding", "permit-port-forwarding", "permit-pty", "private-key-policy"},
		}, false},
		{"bad json", `{"active":`, Status{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStatus([]byte(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !got.ValidUntil.Equal(tt.want.ValidUntil) {
				t.Fatalf("ValidUntil = %v, want %v", got.ValidUntil, tt.want.ValidUntil)
			}
			got.ValidUntil, tt.want.ValidUntil = time.Time{}, time.Time{}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestStatusExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		s           Status
		wantExpired bool
		wantLeft    time.Duration
	}{
		{"not logged in", Status{}, false, 0},
		{"valid", Status{LoggedIn: true, ValidUntil: now.Add(90 * time.Minute)}, false, 90 * time.Minute},
		{"exactly now", Status{LoggedIn: true, ValidUntil: now}, true, 0},
		{"expired", Status{LoggedIn: true, ValidUntil: now.Add(-time.Hour)}, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.Expired(now); got != tt.wantExpired {
				t.Errorf("Expired = %v, want %v", got, tt.wantExpired)
			}
			if got := tt.s.Remaining(now); got != tt.wantLeft {
				t.Errorf("Remaining = %v, want %v", got, tt.wantLeft)
			}
		})
	}
}

func TestParseServers(t *testing.T) {
	got, err := parseServers(fixture(t, "ls.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Server{
		{Name: "0b6a1c3e-7d4f-4a51-9c1e-2f3a4b5c6d03", Hostname: "cache-01", Addr: "10.0.2.21:3022", Version: "17.4.2",
			Labels: map[string]string{"env": "stage"}},
		{Name: "0b6a1c3e-7d4f-4a51-9c1e-2f3a4b5c6d02", Hostname: "db-03", Addr: "", Version: "18.10.0",
			Labels: map[string]string{"env": "prod", "role": "db"}},
		{Name: "0b6a1c3e-7d4f-4a51-9c1e-2f3a4b5c6d01", Hostname: "web-01", Addr: "10.0.1.11:3022", Version: "18.10.0",
			Labels:  map[string]string{"env": "prod", "role": "web", "uptime": "up 3 weeks"},
			Expires: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	for _, in := range []string{"", "  \n", "[]"} {
		got, err := parseServers([]byte(in))
		if err != nil || len(got) != 0 {
			t.Errorf("parseServers(%q) = %v, %v; want empty, nil", in, got, err)
		}
	}
	if _, err := parseServers([]byte(`{"kind":"node"}`)); err == nil {
		t.Error("parseServers(object) succeeded, want error")
	}
	got, err = parseServers([]byte(`[{"kind":"app","metadata":{"name":"x"}}]`))
	if err != nil || len(got) != 0 {
		t.Errorf("non-node kinds should be skipped, got %v, %v", got, err)
	}
}

func TestParsePing(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    PingInfo
		wantErr bool
	}{
		{"local otp", `{"auth":{"type":"local","second_factor":"otp","preferred_local_mfa":"otp","local":{"name":""}},
			"server_version":"18.10.0","min_client_version":"17.0.0-aa","cluster_name":"afr-prod","edition":"community"}`,
			PingInfo{ServerVersion: "18.10.0", MinClientVersion: "17.0.0-aa", ClusterName: "afr-prod", Edition: "community",
				AuthType: "local", SecondFactor: "otp", PreferredLocalMFA: "otp"}, false},
		{"oidc connector", `{"auth":{"type":"oidc","second_factor":"webauthn","oidc":{"name":"okta"}},"server_version":"17.1.0"}`,
			PingInfo{ServerVersion: "17.1.0", AuthType: "oidc", ConnectorName: "okta", SecondFactor: "webauthn"}, false},
		{"missing version", `{"auth":{}}`, PingInfo{}, true},
		{"html", `<html>`, PingInfo{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePing([]byte(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestWebAddr(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"tp.afr.example", "tp.afr.example:443", false},
		{"tp.afr.example:3080", "tp.afr.example:3080", false},
		{"tp.afr.example:3080,3023", "tp.afr.example:3080", false},
		{"https://tp.afr.example/", "tp.afr.example:443", false},
		{"[::1]:3080", "[::1]:3080", false},
		{"", "", true},
		{":443", "", true},
	}
	for _, tt := range tests {
		got, err := WebAddr(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("WebAddr(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}
