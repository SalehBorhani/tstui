#!/bin/sh
# Proves that tstui builds air-gapped: from vendor/ only, with an empty
# module cache, no module proxy, and (on Linux) no network at all.
#
#   scripts/offline-build.sh [output]     (default bin/tstui-offline)
#
# On Linux the build runs in a new network namespace (unshare -rn), which
# has only a loopback device that is down. Where that is not allowed (some
# containers, macOS), it falls back to GOPROXY=off and says so.
set -eu

out=${1:-bin/tstui-offline}
tmp=$(mktemp -d)
trap 'chmod -R u+w "$tmp" 2>/dev/null; rm -rf "$tmp"' EXIT

# An empty module cache and build cache: nothing can come from earlier
# online builds. GOTOOLCHAIN=local stops go from downloading a toolchain.
env_vars="GOFLAGS=-mod=vendor GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
GOMODCACHE=$tmp/modcache GOCACHE=$tmp/gocache CGO_ENABLED=0"

build="go build -trimpath -o $out ./cmd/tstui"

if [ "$(uname -s)" = Linux ] && unshare -rn true 2>/dev/null; then
	echo "offline build: no network (unshare -rn), empty module and build caches"
	# shellcheck disable=SC2086
	unshare -rn env $env_vars sh -c "
		if getent hosts proxy.golang.org >/dev/null 2>&1; then
			echo 'offline build: the network is still reachable' >&2; exit 1
		fi
		$build"
else
	echo "offline build: network namespaces unavailable; using GOPROXY=off and empty caches only"
	# shellcheck disable=SC2086
	env $env_vars sh -c "$build"
fi
echo "offline build: ok -> $out"
