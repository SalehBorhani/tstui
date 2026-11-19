package tsh

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ServerCache keeps the last good listing of each proxy on disk, so that
// tstui can still show what was there, marked stale, when the proxy
// cannot be reached. Files are JSON, one per proxy, mode 0600 in a 0700
// directory: they hold host names, addresses and labels, no secrets. The
// zero value (empty Dir) caches nothing.
type ServerCache struct {
	Dir string
}

// cacheVersion is bumped when the file format changes; other versions
// are ignored, not misread.
const cacheVersion = 1

type cacheFile struct {
	Version  int       `json:"version"`
	Proxy    string    `json:"proxy"`
	ListedAt time.Time `json:"listed_at"`
	Clusters []Cluster `json:"clusters,omitempty"`
	Servers  []Server  `json:"servers"`
}

// ErrNoCache means there is no usable cached listing for the proxy.
var ErrNoCache = errors.New("no cached server list")

func (c ServerCache) path(proxy string) (string, error) {
	if c.Dir == "" {
		return "", ErrNoCache
	}
	key, err := WebAddr(proxy)
	if err != nil {
		return "", err
	}
	// host:port, made safe as a file name (IPv6 brackets and colons too).
	name := strings.NewReplacer(":", "_", "/", "_", "[", "", "]", "", "\\", "_").Replace(key)
	return filepath.Join(c.Dir, name+".json"), nil
}

// Save stores l as proxy's last good listing, listed at at. Per-cluster
// errors are not kept: a cluster that failed has no servers in l.
func (c ServerCache) Save(proxy string, l Listing, at time.Time) error {
	path, err := c.path(proxy)
	if errors.Is(err, ErrNoCache) {
		return nil
	}
	if err != nil {
		return err
	}
	b, err := json.Marshal(cacheFile{Version: cacheVersion, Proxy: proxy, ListedAt: at, Clusters: l.Clusters, Servers: l.Servers})
	if err != nil {
		return fmt.Errorf("encode server cache: %w", err)
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return fmt.Errorf("server cache: %w", err)
	}
	tmp, err := os.CreateTemp(c.Dir, ".servers-*")
	if err != nil {
		return fmt.Errorf("server cache: %w", err)
	}
	defer os.Remove(tmp.Name()) // after a successful rename this fails harmlessly
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write server cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write server cache: %w", err)
	}
	// CreateTemp makes the file 0600 already; the rename keeps that.
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write server cache: %w", err)
	}
	return nil
}

// Load returns proxy's cached listing and when it was made, or ErrNoCache.
func (c ServerCache) Load(proxy string) (Listing, time.Time, error) {
	path, err := c.path(proxy)
	if err != nil {
		return Listing{}, time.Time{}, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Listing{}, time.Time{}, ErrNoCache
	}
	if err != nil {
		return Listing{}, time.Time{}, fmt.Errorf("read server cache: %w", err)
	}
	var f cacheFile
	if err := json.Unmarshal(b, &f); err != nil || f.Version != cacheVersion {
		return Listing{}, time.Time{}, ErrNoCache
	}
	return Listing{Clusters: f.Clusters, Servers: f.Servers}, f.ListedAt, nil
}
