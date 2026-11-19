#!/bin/sh
# Prints the version that builds and packages carry: the latest v* tag
# without its "v" (v0.1.0 -> 0.1.0), plus git describe's suffix between
# tags (0.1.0-3-gabc1234) and -dirty for uncommitted changes. Without a
# tag it prints 0.1.0-dev.
set -eu
if v=$(git describe --tags --match 'v[0-9]*' --dirty 2>/dev/null); then
	echo "${v#v}"
else
	echo "0.1.0-dev"
fi
