package tsh

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerCache(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "servers")
	c := ServerCache{Dir: dir}
	if _, _, err := c.Load("tp.afr.example"); !errors.Is(err, ErrNoCache) {
		t.Fatalf("empty cache: %v", err)
	}
	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	l := Listing{
		Clusters: []Cluster{{Name: "afr", Type: "root", Status: "online"}},
		Servers:  []Server{{Hostname: "web-01", Cluster: "afr", Addr: "10.0.0.1:3022", Labels: map[string]string{"env": "prod"}}},
		Errs:     map[string]error{"hwb": errors.New("boom")},
	}
	if err := c.Save("tp.afr.example:443", l, at); err != nil {
		t.Fatal(err)
	}
	// Same proxy, other spelling.
	got, gotAt, err := c.Load("https://tp.afr.example")
	if err != nil {
		t.Fatal(err)
	}
	if !gotAt.Equal(at) || len(got.Servers) != 1 || got.Servers[0].Labels["env"] != "prod" || got.Clusters[0].Name != "afr" || got.Errs != nil {
		t.Fatalf("loaded %+v at %s", got, gotAt)
	}
	st, err := os.Stat(filepath.Join(dir, "tp.afr.example_443.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("cache file mode %#o, want 0600", st.Mode().Perm())
	}
	if dst, _ := os.Stat(dir); dst.Mode().Perm() != 0o700 {
		t.Errorf("cache dir mode %#o, want 0700", dst.Mode().Perm())
	}
	if _, _, err := c.Load("other.example"); !errors.Is(err, ErrNoCache) {
		t.Errorf("other proxy: %v", err)
	}
	// A file from another format version is ignored.
	if err := os.WriteFile(filepath.Join(dir, "old.example_443.json"), []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Load("old.example"); !errors.Is(err, ErrNoCache) {
		t.Errorf("other version: %v", err)
	}
	// No directory: nothing is cached, nothing fails.
	if err := (ServerCache{}).Save("x", l, at); err != nil {
		t.Errorf("zero cache Save: %v", err)
	}
}
