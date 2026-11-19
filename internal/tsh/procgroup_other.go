//go:build !unix

package tsh

import "os/exec"

// setProcessGroup is a no-op where process groups are not available; the
// default cancellation kills only the direct child.
func setProcessGroup(*exec.Cmd) {}
