#!/bin/sh
# Builds the release artifacts into dist/:
#
#   tstui_<version>_<os>_<arch>          the plain binary
#   tstui_<version>_<os>_<arch>.tar.gz   the binary with docs, examples, LICENSE
#   tstui_<version>_<arch>.deb           Linux only
#   SHA256SUMS
#
# for linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64.
#
# It needs only the Go toolchain, tar, gzip and sha256sum (or shasum), so
# it runs air-gapped; dpkg-deb is needed for the .deb files and they are
# skipped without it. Every binary is static (CGO_ENABLED=0), built from
# vendor/, and checked for it.
#
# Settings (environment): VERSION, COMMIT, DATE (as in the Makefile),
# TARGETS (default "linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"),
# MAINTAINER (the .deb Maintainer field).
set -eu

cd "$(dirname "$0")/.."

VERSION=${VERSION:-$(scripts/version.sh)}
COMMIT=${COMMIT:-$(git rev-parse --short=12 HEAD 2>/dev/null || true)}
DATE=${DATE:-$(git log -1 --format=%cI 2>/dev/null || true)}
TARGETS=${TARGETS:-"linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"}
MAINTAINER=${MAINTAINER:-"tstui maintainers <tstui@users.noreply.github.com>"}

# File times in the archives: the commit time, so a rebuild of the same
# commit gives the same archives.
EPOCH=${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || date +%s)}

LDFLAGS="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE"
DIST=dist
STAGE=$DIST/stage

die() { echo "package: $*" >&2; exit 1; }

case $VERSION in
[0-9]*) ;;
*) die "version $VERSION must start with a digit (tag as v0.1.0)" ;;
esac

if command -v sha256sum >/dev/null; then
	sha256() { sha256sum "$@"; }
else
	sha256() { shasum -a 256 "$@"; }
fi

# GNU tar can make the archive reproducible; bsdtar (macOS) cannot.
if tar --version 2>/dev/null | grep -q 'GNU tar'; then
	tar_c() { tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$EPOCH" -cf - "$@"; }
else
	tar_c() { tar -cf - "$@"; }
fi

rm -rf "$DIST"
mkdir -p "$STAGE"

# The licenses of everything linked into the binary: the Go standard
# library and every vendored module (vendor/ also holds the patched copy
# of charmbracelet/x/vt from third_party/).
{
	printf 'tstui links the following third-party software. Each license\n'
	printf 'applies to the module named above it.\n'
	printf '\n================================================================\n'
	printf 'Go standard library (%s)\n\n' "$(go env GOVERSION)"
	# Some distributions move it out of GOROOT. golang.org/x/sys carries
	# the identical text ("Copyright 2009 The Go Authors").
	golicense=$(go env GOROOT)/LICENSE
	[ -f "$golicense" ] || golicense=vendor/golang.org/x/sys/LICENSE
	cat "$golicense"
	find vendor -type f \( -iname 'LICENSE*' -o -iname 'NOTICE*' -o -iname 'COPYING*' \) | sort |
		while read -r f; do
			rel=${f#vendor/}
			printf '\n================================================================\n'
			printf '%s (%s)\n\n' "${rel%/*}" "${rel##*/}"
			cat "$f"
		done
} >"$STAGE/THIRD_PARTY_LICENSES"

# The man page, gzipped once for the .deb.
gzip -9n -c docs/tstui.1 >"$STAGE/tstui.1.gz"

for target in $TARGETS; do
	os=${target%/*}
	arch=${target#*/}
	name=tstui_${VERSION}_${os}_${arch}
	dir=$STAGE/$name
	mkdir -p "$dir"

	echo "build $os/$arch"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -mod=vendor -trimpath \
		-ldflags "$LDFLAGS" -o "$dir/tstui" ./cmd/tstui

	# Static check that works for any target: the build settings recorded
	# in the binary. make check-static also runs ldd on the host binary.
	go version -m "$dir/tstui" | grep -q 'CGO_ENABLED=0' ||
		die "$name: not built with CGO_ENABLED=0"

	cp "$dir/tstui" "$DIST/$name" # the plain binary, for a direct download

	mkdir -p "$dir/examples"
	cp README.md CHANGELOG.md LICENSE NOTICE docs/tstui.1 "$STAGE/THIRD_PARTY_LICENSES" "$dir/"
	cp -R examples/config.yaml examples/skins "$dir/examples/"

	(cd "$STAGE" && tar_c "$name") | gzip -9n >"$DIST/$name.tar.gz"

	if [ "$os" = linux ]; then
		if ! command -v dpkg-deb >/dev/null; then
			echo "skip $arch .deb: dpkg-deb not found"
			continue
		fi
		# Debian versions sort "~" before the release: 0.1.0~dev < 0.1.0.
		debversion=$(echo "$VERSION" | tr '-' '~')
		root=$STAGE/deb-$arch
		mkdir -p "$root/DEBIAN" "$root/usr/bin" "$root/usr/share/man/man1" \
			"$root/usr/share/doc/tstui/examples"
		install -m 0755 "$dir/tstui" "$root/usr/bin/tstui"
		install -m 0644 "$STAGE/tstui.1.gz" "$root/usr/share/man/man1/tstui.1.gz"
		install -m 0644 README.md "$root/usr/share/doc/tstui/README.md"
		{
			printf 'Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/\n'
			printf 'Upstream-Name: tstui\n\nFiles: *\nCopyright: 2026 ali\nLicense: Apache-2.0\n'
			printf ' On Debian systems the full text is in /usr/share/common-licenses/Apache-2.0.\n\n'
			cat NOTICE
		} >"$root/usr/share/doc/tstui/copyright"
		chmod 0644 "$root/usr/share/doc/tstui/copyright"
		install -m 0644 "$STAGE/THIRD_PARTY_LICENSES" "$root/usr/share/doc/tstui/THIRD_PARTY_LICENSES"
		gzip -9n -c CHANGELOG.md >"$root/usr/share/doc/tstui/changelog.gz"
		chmod 0644 "$root/usr/share/doc/tstui/changelog.gz"
		cp -R examples/config.yaml examples/skins "$root/usr/share/doc/tstui/examples/"
		find "$root/usr/share/doc/tstui/examples" -type d -exec chmod 0755 {} +
		find "$root/usr/share/doc/tstui/examples" -type f -exec chmod 0644 {} +
		size=$(du -sk "$root/usr" | cut -f1)
		cat >"$root/DEBIAN/control" <<EOF
Package: tstui
Version: $debversion
Architecture: $arch
Maintainer: $MAINTAINER
Installed-Size: $size
Section: admin
Priority: optional
Suggests: teleport
Homepage: https://github.com/root-ali/tstui
Description: k9s-style terminal UI for Teleport SSH
 tstui wraps tsh, Teleport's command-line client, in a terminal UI: a
 server list per proxy and cluster, an embedded SSH terminal with tabs
 and split panes, a login popup with MFA, file transfer, port forwards
 and multi-exec. It needs tsh on PATH.
EOF
		find "$root" -exec touch -h -d "@$EPOCH" {} +
		SOURCE_DATE_EPOCH=$EPOCH dpkg-deb --root-owner-group -Zgzip --build "$root" \
			"$DIST/tstui_${debversion}_${arch}.deb" >/dev/null
		echo "deb   $DIST/tstui_${debversion}_${arch}.deb"
	fi
done

rm -rf "$STAGE"
(
	cd "$DIST"
	set --
	for f in *; do [ -f "$f" ] && set -- "$@" "$f"; done
	sha256 "$@" >SHA256SUMS
)
echo "release artifacts in $DIST/:"
ls -l "$DIST"
