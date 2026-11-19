package multiexec

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// Risky returns why command looks destructive, one reason per pattern
// found, or nil. It errs on the side of warning: a risky word anywhere
// counts (so `sudo rm -rf x`, `bash -c "reboot"` and `xargs rm -rf` are
// caught), and a false alarm only means a stronger confirmation.
func Risky(command string) []string {
	found := map[string]bool{}
	for _, re := range rawPatterns {
		if re.re.MatchString(command) {
			found[re.reason] = true
		}
	}
	for _, seg := range segments(command) {
		for i, w := range seg {
			name := path.Base(w)
			if strings.HasPrefix(name, "mkfs") {
				name = "mkfs"
			}
			if reason, ok := alwaysRisky[name]; ok {
				found[reason] = true
				continue
			}
			rest := seg[i+1:]
			switch name {
			case "rm":
				if rmRecursive(rest) {
					found["recursive delete (rm -r/-rf)"] = true
				}
			case "dd":
				found["raw disk copy (dd)"] = true
			case "init", "telinit":
				if len(rest) > 0 && (rest[0] == "0" || rest[0] == "6") {
					found["shutdown/reboot (init "+rest[0]+")"] = true
				}
			case "systemctl":
				for _, a := range rest {
					if systemctlRisky[a] {
						found["shutdown/reboot (systemctl "+a+")"] = true
					}
				}
			case "chmod", "chown", "chgrp":
				if hasFlag(rest, 'R', "--recursive") && contains(rest, "/") {
					found["recursive "+name+" on /"] = true
				}
			case "kill":
				if contains(rest, "-1") {
					found["kill every process (kill -1)"] = true
				}
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	out := make([]string, 0, len(found))
	for r := range found {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

var alwaysRisky = map[string]string{
	"shutdown": "shutdown/reboot (shutdown)",
	"reboot":   "shutdown/reboot (reboot)",
	"halt":     "shutdown/reboot (halt)",
	"poweroff": "shutdown/reboot (poweroff)",
	"mkfs":     "filesystem creation (mkfs)",
	"mke2fs":   "filesystem creation (mke2fs)",
	"mkswap":   "filesystem creation (mkswap)",
	"wipefs":   "disk wipe (wipefs)",
	"shred":    "disk wipe (shred)",
	"fdisk":    "partitioning (fdisk)",
	"sfdisk":   "partitioning (sfdisk)",
	"parted":   "partitioning (parted)",
	"sgdisk":   "partitioning (sgdisk)",
	"killall5": "kill every process (killall5)",
}

var systemctlRisky = map[string]bool{
	"reboot": true, "poweroff": true, "halt": true, "kexec": true,
	"rescue": true, "emergency": true, "suspend": true, "hibernate": true,
}

var rawPatterns = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`>\s*/dev/(sd|hd|vd|xvd|nvme|mmcblk|mapper/|disk/)`), "write to a disk device"},
	{regexp.MustCompile(`:\s*\(\s*\)\s*\{[^}]*:\s*\|\s*:`), "fork bomb"},
}

// segments splits a command into simple commands (at ; | & newlines and
// subshell or quote characters) and those into words. It is not a shell
// parser; quotes are dropped so `bash -c "rm -rf /"` shows its words.
func segments(command string) [][]string {
	var out [][]string
	split := strings.FieldsFunc(command, func(r rune) bool {
		return strings.ContainsRune(";|&\n", r)
	})
	clean := strings.NewReplacer("'", " ", `"`, " ", "`", " ", "$(", " ", "(", " ", ")", " ", "{", " ", "}", " ", "\\", "")
	for _, s := range split {
		if words := strings.Fields(clean.Replace(s)); len(words) > 0 {
			out = append(out, words)
		}
	}
	return out
}

// rmRecursive reports whether rm's arguments include -r, -R or --recursive
// (alone or combined, as in -rf or -fR).
func rmRecursive(args []string) bool {
	return hasFlag(args, 'r', "--recursive") || hasFlag(args, 'R', "--recursive")
}

func hasFlag(args []string, short rune, long string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == long {
			return true
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(a[1:], short) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
