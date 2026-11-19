package tsh

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// DefaultHome returns the tsh home directory: $TELEPORT_HOME if set in
// environ, otherwise ~/.tsh.
func DefaultHome(environ []string) string {
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, "TELEPORT_HOME="); ok && v != "" {
			return v
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".tsh"
	}
	return filepath.Join(home, ".tsh")
}

type profileYAML struct {
	WebProxyAddr string `yaml:"web_proxy_addr"`
	User         string `yaml:"user"`
	Cluster      string `yaml:"cluster"`
	MFAMode      string `yaml:"mfa_mode"`
}

// ReadProfiles reads the profile files (*.yaml) in a tsh home directory.
// A missing directory yields no profiles and no error.
func ReadProfiles(home string) ([]Profile, error) {
	paths, err := filepath.Glob(filepath.Join(home, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("list tsh profiles in %s: %w", home, err)
	}
	current := ""
	if b, err := os.ReadFile(filepath.Join(home, "current-profile")); err == nil {
		current = strings.TrimSpace(string(b))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read current tsh profile: %w", err)
	}

	profiles := make([]Profile, 0, len(paths))
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read tsh profile %s: %w", p, err)
		}
		var y profileYAML
		if err := yaml.Unmarshal(b, &y); err != nil {
			return nil, fmt.Errorf("parse tsh profile %s: %w", p, err)
		}
		name := strings.TrimSuffix(filepath.Base(p), ".yaml")
		profiles = append(profiles, Profile{
			Name:         name,
			WebProxyAddr: y.WebProxyAddr,
			User:         y.User,
			Cluster:      y.Cluster,
			MFAMode:      y.MFAMode,
			Current:      name == current,
		})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}
