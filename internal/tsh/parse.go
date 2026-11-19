package tsh

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// CleanOutput strips ANSI escapes and surrounding whitespace from tsh output.
// tsh colours its "ERROR:" prefix even when stderr is not a terminal.
func CleanOutput(b []byte) string {
	return strings.TrimSpace(ansiRE.ReplaceAllString(string(b), ""))
}

// classifyError maps tsh's stderr text to one of the package's sentinel
// errors, keeping the cleaned message for the user.
func classifyError(stderr []byte) error {
	msg := strings.TrimPrefix(CleanOutput(stderr), "ERROR: ")
	if msg == "" {
		msg = "tsh failed without an error message"
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "not logged in"),
		strings.Contains(lower, "profile expired"),
		strings.Contains(lower, "cert has expired"),
		strings.Contains(lower, "certificate has expired"):
		return fmt.Errorf("%w: %s", ErrNotLoggedIn, msg)
	case strings.Contains(lower, "access denied"):
		return fmt.Errorf("%w: %s", ErrAccessDenied, msg)
	}
	return fmt.Errorf("tsh: %s", msg)
}

func parseVersion(b []byte) (VersionInfo, error) {
	var v VersionInfo
	if err := json.Unmarshal(b, &v); err != nil {
		return VersionInfo{}, fmt.Errorf("parse tsh version output: %w", err)
	}
	if v.Version == "" {
		return VersionInfo{}, fmt.Errorf("parse tsh version output: no version field")
	}
	return v, nil
}

type statusJSON struct {
	Active *profileStatusJSON `json:"active"`
}

type profileStatusJSON struct {
	ProfileURL string    `json:"profile_url"`
	Username   string    `json:"username"`
	Cluster    string    `json:"cluster"`
	Roles      []string  `json:"roles"`
	Logins     []string  `json:"logins"`
	ValidUntil time.Time `json:"valid_until"`
	Extensions []string  `json:"extensions"`
}

// parseStatus parses `tsh status --format=json`. Empty input means no
// profile exists, which is reported as LoggedIn=false.
func parseStatus(b []byte) (Status, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return Status{}, nil
	}
	var s statusJSON
	if err := json.Unmarshal(b, &s); err != nil {
		return Status{}, fmt.Errorf("parse tsh status output: %w", err)
	}
	if s.Active == nil {
		return Status{}, nil
	}
	a := s.Active
	return Status{
		LoggedIn:   true,
		ProfileURL: a.ProfileURL,
		User:       a.Username,
		Cluster:    a.Cluster,
		Roles:      a.Roles,
		Logins:     a.Logins,
		ValidUntil: a.ValidUntil,
		Extensions: a.Extensions,
	}, nil
}

// serverJSON is the types.ServerV2 resource as printed by `tsh ls --format=json`.
type serverJSON struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name    string            `json:"name"`
		Labels  map[string]string `json:"labels"`
		Expires *time.Time        `json:"expires"`
	} `json:"metadata"`
	Spec struct {
		Addr      string `json:"addr"`
		Hostname  string `json:"hostname"`
		Version   string `json:"version"`
		CmdLabels map[string]struct {
			Result string `json:"result"`
		} `json:"cmd_labels"`
	} `json:"spec"`
}

func parseServers(b []byte) ([]Server, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	var raw []serverJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse tsh ls output: %w", err)
	}
	servers := make([]Server, 0, len(raw))
	for _, r := range raw {
		if r.Kind != "" && r.Kind != "node" {
			continue
		}
		labels := make(map[string]string, len(r.Metadata.Labels)+len(r.Spec.CmdLabels))
		for k, v := range r.Metadata.Labels {
			labels[k] = v
		}
		for k, v := range r.Spec.CmdLabels {
			labels[k] = strings.TrimSpace(v.Result)
		}
		srv := Server{
			Name:     r.Metadata.Name,
			Hostname: r.Spec.Hostname,
			Addr:     r.Spec.Addr,
			Version:  r.Spec.Version,
			Labels:   labels,
		}
		if r.Metadata.Expires != nil {
			srv.Expires = *r.Metadata.Expires
		}
		servers = append(servers, srv)
	}
	sort.SliceStable(servers, func(i, j int) bool { return servers[i].Hostname < servers[j].Hostname })
	return servers, nil
}

// clusterJSON is one entry of `tsh clusters --format=json` (verified,
// v18.11.3).
type clusterJSON struct {
	Name     string `json:"cluster_name"`
	Status   string `json:"status"`
	Type     string `json:"cluster_type"`
	Selected bool   `json:"selected"`
}

// parseClusters reads `tsh clusters --format=json`: the root cluster
// first, then the leaf clusters by name.
func parseClusters(b []byte) ([]Cluster, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	var raw []clusterJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse tsh clusters output: %w", err)
	}
	out := make([]Cluster, 0, len(raw))
	for _, r := range raw {
		if r.Name == "" {
			continue
		}
		out = append(out, Cluster(r))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Root() != out[j].Root() {
			return out[i].Root()
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

type connectorJSON struct {
	Name string `json:"name"`
}

type pingJSON struct {
	ServerVersion    string `json:"server_version"`
	MinClientVersion string `json:"min_client_version"`
	ClusterName      string `json:"cluster_name"`
	Edition          string `json:"edition"`
	Auth             struct {
		Type              string         `json:"type"`
		SecondFactor      string         `json:"second_factor"`
		PreferredLocalMFA string         `json:"preferred_local_mfa"`
		Local             *connectorJSON `json:"local"`
		OIDC              *connectorJSON `json:"oidc"`
		SAML              *connectorJSON `json:"saml"`
		Github            *connectorJSON `json:"github"`
	} `json:"auth"`
}

func parsePing(b []byte) (PingInfo, error) {
	var p pingJSON
	if err := json.Unmarshal(b, &p); err != nil {
		return PingInfo{}, fmt.Errorf("parse /webapi/ping response: %w", err)
	}
	if p.ServerVersion == "" {
		return PingInfo{}, fmt.Errorf("parse /webapi/ping response: no server_version field")
	}
	info := PingInfo{
		ServerVersion:     p.ServerVersion,
		MinClientVersion:  p.MinClientVersion,
		ClusterName:       p.ClusterName,
		Edition:           p.Edition,
		AuthType:          p.Auth.Type,
		SecondFactor:      p.Auth.SecondFactor,
		PreferredLocalMFA: p.Auth.PreferredLocalMFA,
	}
	for _, c := range []*connectorJSON{p.Auth.Local, p.Auth.OIDC, p.Auth.SAML, p.Auth.Github} {
		if c != nil && c.Name != "" {
			info.ConnectorName = c.Name
			break
		}
	}
	return info, nil
}
