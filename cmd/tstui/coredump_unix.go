//go:build unix

package main

import "syscall"

// disableCoreDumps sets the soft core file size limit to zero for tstui
// and the processes it starts (tsh holds the password briefly too), so a
// crash never writes memory, secrets included, to a core file. Best
// effort: on failure the limit stays as it was.
func disableCoreDumps() {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &lim); err != nil {
		return
	}
	lim.Cur = 0
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &lim)
}
