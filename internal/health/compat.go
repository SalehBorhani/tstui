// Package health tracks the health of proxies and nodes: per-proxy
// latency, reachability, certificate expiry and client/server version
// compatibility, and a per-node reachability marker taken from `tsh ls`.
// Everything here is pure or works through small interfaces, so the UI
// only renders what it is given.
package health

import (
	"fmt"
	"strconv"
	"strings"
)

// CompatLevel says how well the local tsh matches a proxy's version.
type CompatLevel int

// Compatibility levels.
const (
	// CompatUnknown: a version is missing or unreadable.
	CompatUnknown CompatLevel = iota
	// CompatOK: a supported combination.
	CompatOK
	// CompatWarn: not supported by Teleport; things may fail.
	CompatWarn
)

// Compat is the result of CheckVersions.
type Compat struct {
	Level CompatLevel
	// Message explains a warning or an unknown result; empty when OK.
	Message string
	// Short is a few words for a narrow header column.
	Short string
}

// CheckVersions applies Teleport's documented client compatibility rule
// (https://goteleport.com/docs/upgrading/overview/, "Component
// compatibility", read 2026-11-18): "Servers support clients that are one
// major version behind, but do not support clients that are on a newer
// major version." So a server on major N supports tsh on N and N-1; minor
// and patch differences do not matter. On top of that the proxy
// advertises min_client_version in /webapi/ping (tsh itself warns below
// it), and a client under that minimum is reported too.
//
// This is the only place the rule lives.
func CheckVersions(client, server, minClient string) Compat {
	cv, cerr := ParseVersion(client)
	sv, serr := ParseVersion(server)
	if cerr != nil || serr != nil {
		return Compat{Level: CompatUnknown, Message: "version unknown", Short: "version unknown"}
	}
	switch {
	case cv.Major > sv.Major:
		return Compat{Level: CompatWarn, Message: fmt.Sprintf(
			"tsh v%s is newer than the server (v%s); Teleport does not support newer-major clients", trimV(client), trimV(server)),
			Short: fmt.Sprintf("tsh v%d newer than server v%d", cv.Major, sv.Major)}
	case cv.Major < sv.Major-1:
		return Compat{Level: CompatWarn, Message: fmt.Sprintf(
			"tsh v%s is too old for the server (v%s); use tsh v%d or v%d", trimV(client), trimV(server), sv.Major-1, sv.Major),
			Short: fmt.Sprintf("tsh v%d too old for server v%d", cv.Major, sv.Major)}
	}
	if mv, err := ParseVersion(minClient); err == nil && cv.Less(mv) {
		return Compat{Level: CompatWarn, Message: fmt.Sprintf(
			"tsh v%s is older than the server's minimum client version v%s", trimV(client), trimV(minClient)),
			Short: "tsh below min v" + trimV(minClient)}
	}
	return Compat{Level: CompatOK}
}

// The tsh versions tstui itself works with. tstui reads tsh's JSON and
// answers its login prompts, and both can change between major versions.
// TestedTshMajor is the one verified against a real tsh and cluster
// (docs/tsh-notes.md); MinTshMajor is the oldest one assumed to print the
// same shapes, from Teleport's docs. Neither has anything to do with
// which servers a tsh can talk to: that is CheckVersions.
const (
	MinTshMajor    = 16
	TestedTshMajor = 18
)

// CheckClient says whether tstui supports the local tsh version: a warning
// below MinTshMajor (unsupported) and above TestedTshMajor (untested).
func CheckClient(version string) Compat {
	v, err := ParseVersion(version)
	if err != nil {
		return Compat{Level: CompatUnknown, Message: "tsh version unknown", Short: "version unknown"}
	}
	switch {
	case v.Major < MinTshMajor:
		return Compat{Level: CompatWarn, Message: fmt.Sprintf(
			"tsh v%s is not supported: tstui needs tsh v%d or newer (tested with v%d); listing and login may fail",
			trimV(version), MinTshMajor, TestedTshMajor), Short: fmt.Sprintf("tsh v%d unsupported", v.Major)}
	case v.Major > TestedTshMajor:
		return Compat{Level: CompatWarn, Message: fmt.Sprintf(
			"tsh v%s is newer than the tsh tstui was tested with (v%d); if login or listing fails, please report it",
			trimV(version), TestedTshMajor), Short: fmt.Sprintf("tsh v%d untested", v.Major)}
	}
	return Compat{Level: CompatOK}
}

func trimV(s string) string { return strings.TrimPrefix(strings.TrimSpace(s), "v") }

// Version is a semantic version: MAJOR.MINOR.PATCH[-PRERELEASE].
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

// ParseVersion reads "18.10.0", "v18.10.0" or "17.0.0-aa". Missing minor
// or patch parts count as 0. Build metadata (+...) is ignored.
func ParseVersion(s string) (Version, error) {
	s = trimV(s)
	if s == "" {
		return Version{}, fmt.Errorf("empty version")
	}
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) > 3 {
		return Version{}, fmt.Errorf("invalid version %q", s)
	}
	var nums [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("invalid version %q", s)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre}, nil
}

// Less orders versions by semver precedence: a pre-release comes before
// its release (17.0.0-aa < 17.0.0). Pre-release strings compare as text.
func (v Version) Less(o Version) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	if v.Patch != o.Patch {
		return v.Patch < o.Patch
	}
	switch {
	case v.Pre == o.Pre, v.Pre == "":
		return false
	case o.Pre == "":
		return true
	}
	return v.Pre < o.Pre
}
