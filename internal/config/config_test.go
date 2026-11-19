package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

const fullConfig = `
default_proxy: a-teleport
proxies:
  - name: a-teleport
    addr: tp.afr.example:443
    shortcut: a
    user: ali
  - name: b-teleport
    addr: tp.ast.example
    shortcut: b
    auth: okta
favorites:
  a-teleport: [web-01, web-02]
skin: dracula
keybindings:
  focus: Ctrl-A
terminal:
  scrollback: 5000
  auto_reconnect: true
  mouse: false
`

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    *Config
		wantErr []string
	}{
		{
			name: "empty document gives defaults",
			in:   "",
			want: Default(),
		},
		{
			name: "full",
			in:   fullConfig,
			want: &Config{
				DefaultProxy: "a-teleport",
				Proxies: []Proxy{
					{Name: "a-teleport", Addr: "tp.afr.example:443", Shortcut: "a", User: "ali"},
					{Name: "b-teleport", Addr: "tp.ast.example", Shortcut: "b", Auth: "okta"},
				},
				Favorites:   map[string][]string{"a-teleport": {"web-01", "web-02"}},
				Skin:        "dracula",
				Keybindings: Keybindings{Focus: "Ctrl-A"},
				Terminal:    Terminal{Scrollback: 5000, AutoReconnect: true, Mouse: new(false)},
				Exec:        Exec{Concurrency: DefaultExecConcurrency},
			},
		},
		{
			name: "port forwards and exec",
			in: `
port_forwards:
  - name: pg
    host: db-03
    login: ali
    forward: 5432:localhost:5432
  - name: web
    proxy: a-teleport
    host: web-01
    forward: 0.0.0.0:8080:10.0.0.5:80
exec:
  concurrency: 10
  timeout: 2m
`,
			want: func() *Config {
				c := Default()
				c.PortForwards = []PortForward{
					{Name: "pg", Host: "db-03", Login: "ali", Forward: "5432:localhost:5432"},
					{Name: "web", Proxy: "a-teleport", Host: "web-01", Forward: "0.0.0.0:8080:10.0.0.5:80"},
				}
				c.Exec = Exec{Concurrency: 10, Timeout: "2m"}
				return c
			}(),
		},
		{
			name: "bad port forwards and exec",
			in: `
port_forwards:
  - name: pg
    host: -oProxyCommand=x
    forward: 5432
  - name: pg
    host: db
    login: a b
    forward: 1:h:2
  - host: db
    forward: 1:h:2
exec:
  concurrency: 500
  timeout: soon
`,
			wantErr: []string{
				`port forward "pg": host "-oProxyCommand=x" is not a valid server name`,
				`port forward "pg": forward "5432": want [bind:]localport:host:port`,
				`port forward "pg": duplicate name`,
				`port forward "pg": login "a b" is not valid`,
				`port_forwards[2]: name is required`,
				`exec.concurrency 500 must be between 1 and 50`,
				`exec.timeout "soon" must be a positive duration`,
			},
		},
		{
			name:    "unknown key",
			in:      "default_proxi: a\n",
			wantErr: []string{"field default_proxi not found"},
		},
		{
			name: "every proxy problem is reported",
			in: `
proxies:
  - name: a
    addr: https://tp.example
    shortcut: A
  - name: a
    addr: ""
    shortcut: b
  - name: c
    addr: tp.c.example
    shortcut: b
  - addr: tp.d.example
`,
			wantErr: []string{
				`proxy "a": addr: "https://tp.example" must be host[:port], without a scheme`,
				`shortcut "A" must be a single lowercase letter`,
				`proxy "a": duplicate name`,
				`proxy "a": addr: is required`,
				`proxy "c": shortcut "b" is already used by proxy "a"`,
				`proxies[3]: name is required`,
			},
		},
		{
			name:    "default proxy must exist",
			in:      "default_proxy: nope\n",
			wantErr: []string{`default_proxy "nope" does not match any proxy name`},
		},
		{
			name:    "favorites must name a proxy",
			in:      "favorites:\n  nope: [x]\n",
			wantErr: []string{`favorites: "nope" does not match any proxy name`},
		},
		{
			name:    "bad focus key",
			in:      "keybindings:\n  focus: ctrl+b\n",
			wantErr: []string{`keybindings.focus "ctrl+b" must look like Ctrl-B or Alt-X`},
		},
		{
			name:    "scrollback out of range",
			in:      "terminal:\n  scrollback: -1\n",
			wantErr: []string{"terminal.scrollback -1 must be between 0 and 1000000"},
		},
		{
			name:    "bad skin name",
			in:      "skin: ../etc/passwd\n",
			wantErr: []string{`skin "../etc/passwd" is not a valid skin name`},
		},
		{
			name:    "bad yaml",
			in:      "proxies: [\n",
			wantErr: []string{"parse:"},
		},
		{
			name: "tsh prompt patterns",
			in:   "tsh_prompts:\n  password: ['^Passwort:']\n",
			want: func() *Config {
				c := Default()
				c.TshPrompts = map[string][]string{"password": {"^Passwort:"}}
				return c
			}(),
		},
		{
			name:    "bad tsh prompt pattern",
			in:      "tsh_prompts:\n  otp: ['(']\n",
			wantErr: []string{"tsh_prompts.otp: error parsing regexp"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(strings.NewReader(tt.in))
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("got no error, want %q", tt.wantErr)
				}
				for _, w := range tt.wantErr {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error %q\nlacks %q", err, w)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestExecTimeout(t *testing.T) {
	for in, want := range map[string]time.Duration{"": DefaultExecTimeout, "90s": 90 * time.Second, "bad": DefaultExecTimeout} {
		if got := (Exec{Timeout: in}).TimeoutDuration(); got != want {
			t.Errorf("TimeoutDuration(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestStateDir(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if d, _ := StateDir(env(map[string]string{"XDG_STATE_HOME": "/x", "HOME": "/h"})); d != "/x/tstui" {
		t.Errorf("XDG: %s", d)
	}
	if d, _ := StateDir(env(map[string]string{"XDG_STATE_HOME": "rel", "HOME": "/h"})); d != "/h/.local/state/tstui" {
		t.Errorf("fallback: %s", d)
	}
}

func TestTerminalDefaults(t *testing.T) {
	c := Default()
	if c.Terminal.Scrollback != DefaultScrollback || c.Terminal.AutoReconnect || !c.Terminal.MouseEnabled() {
		t.Fatalf("terminal defaults %+v", c.Terminal)
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg, warnings, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load = %v, %v", warnings, err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Fatalf("got %+v, want defaults", cfg)
	}
}

func TestLoadWarnsOnLoosePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(fullConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	_, warnings, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "chmod 600") {
		t.Fatalf("warnings = %q", warnings)
	}

	if err := os.Chmod(path, FileMode); err != nil {
		t.Fatal(err)
	}
	if _, warnings, _ = Load(path); len(warnings) != 0 {
		t.Fatalf("warnings for 0600 file = %q", warnings)
	}
}

func TestLoadErrorNamesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("default_proxy: x\n"), FileMode); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want it to name %s", err, path)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	cfg, err := Parse(strings.NewReader(fullConfig))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Favorites["b-teleport"] = []string{"db-03"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != FileMode {
		t.Fatalf("mode = %#o, want %#o", st.Mode().Perm(), FileMode)
	}
	if dst, _ := os.Stat(filepath.Dir(path)); dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %#o, want 0700", dst.Mode().Perm())
	}

	got, warnings, err := Load(path)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load = %v, %v", warnings, err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("round trip:\ngot  %+v\nwant %+v", got, cfg)
	}

	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := Default()
	cfg.DefaultProxy = "missing"
	if err := Save(path, cfg); err == nil {
		t.Fatal("saved an invalid config")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("invalid config written to disk")
	}
}

func TestDefaultPath(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"xdg", map[string]string{"XDG_CONFIG_HOME": "/x/cfg", "HOME": "/home/ali"}, "/x/cfg/tstui/config.yaml"},
		{"relative xdg is ignored", map[string]string{"XDG_CONFIG_HOME": "cfg", "HOME": "/home/ali"}, "/home/ali/.config/tstui/config.yaml"},
		{"home", map[string]string{"HOME": "/home/ali"}, "/home/ali/.config/tstui/config.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DefaultPath(env(tt.env))
			if err != nil || got != tt.want {
				t.Fatalf("DefaultPath = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestProxyByName(t *testing.T) {
	cfg, err := Parse(strings.NewReader(fullConfig))
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := cfg.ProxyByName("b-teleport"); !ok || p.Auth != "okta" {
		t.Fatalf("ProxyByName = %+v, %v", p, ok)
	}
	if _, ok := cfg.ProxyByName("zzz"); ok {
		t.Fatal("found a proxy that does not exist")
	}
}

// examples/config.yaml documents every key, so it must parse and must
// show each one: a key added to Config without an example fails here.
func TestExampleConfig(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "config.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(bytes.NewReader(raw)); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	var keys func(prefix string, v any)
	keys = func(prefix string, v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, sub := range v {
				present[prefix+k] = true
				keys(prefix+k+".", sub)
			}
		case []any:
			for _, sub := range v {
				keys(prefix, sub)
			}
		}
	}
	keys("", doc)

	var missing []string
	var walk func(prefix string, t reflect.Type)
	walk = func(prefix string, t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return
		}
		for i := range t.NumField() {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
			if !present[prefix+name] {
				missing = append(missing, prefix+name)
			}
			walk(prefix+name+".", t.Field(i).Type)
		}
	}
	walk("", reflect.TypeFor[Config]())
	if len(missing) > 0 {
		t.Errorf("%s does not show these keys: %s", path, strings.Join(missing, ", "))
	}
}
