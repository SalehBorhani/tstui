BINARY  := tstui
PKG     := ./cmd/tstui
# Build information (see scripts/version.sh and main.versionLine).
VERSION ?= $(shell scripts/version.sh)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
DATE    ?= $(shell git log -1 --format=%cI 2>/dev/null)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
GOFLAGS := -mod=vendor -trimpath
export CGO_ENABLED := 0

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
# The release artifacts (scripts/package.sh). Windows is not supported:
# the PTY layer (login, sessions, forwards) is Unix-only.
RELEASE_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: all build test race vet lint fmt vendor tidy cross check-static offline-build \
	release ci clean run-fake run-demo

all: vet test build

build:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(PKG)

test:
	go test -mod=vendor ./...

# The race detector needs cgo.
race:
	CGO_ENABLED=1 go test -mod=vendor -race ./...

vet:
	go vet -mod=vendor ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w cmd internal

tidy:
	go mod tidy

vendor: tidy
	go mod vendor

cross:
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		echo "building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
			-o dist/$(BINARY)-$$os-$$arch $(PKG) || exit 1; \
	done

# The host binary must have no dynamic dependencies (Linux: ldd).
check-static: build
	@case "$$(uname -s)" in \
	Linux) out=$$(ldd bin/$(BINARY) 2>&1 || true); echo "ldd: $$out"; \
		case "$$out" in *"not a dynamic executable"*|*"statically linked"*) ;; \
		*) echo "bin/$(BINARY) is dynamically linked" >&2; exit 1;; esac;; \
	*) otool -L bin/$(BINARY) 2>/dev/null || true;; \
	esac
	@go version -m bin/$(BINARY) | grep -q 'CGO_ENABLED=0' && echo "static: ok (CGO_ENABLED=0)"

# Build from vendor/ with no network and empty Go caches.
offline-build:
	scripts/offline-build.sh bin/$(BINARY)-offline

# Plain binaries and .tar.gz for linux/amd64, linux/arm64, darwin/amd64 and
# darwin/arm64, .deb for Linux, and SHA256SUMS, in dist/. Tag first
# (git tag v0.1.0) for a clean version.
release:
	VERSION='$(VERSION)' COMMIT='$(COMMIT)' DATE='$(DATE)' TARGETS='$(RELEASE_TARGETS)' \
		scripts/package.sh

# What CI runs, minus the race tests (they need cgo; run make race).
ci: vet lint test check-static offline-build

# Run against the fake tsh: make run-fake STATE=expired
STATE ?= valid
run-fake: build
	PATH="$(CURDIR)/testdata/fake-tsh:$$PATH" FAKE_TSH_STATE=$(STATE) TELEPORT_HOME=$$(mktemp -d) \
		./bin/$(BINARY) --proxy=127.0.0.1:1

# The embedded terminal without a cluster: a local bash in a tab, and Enter
# on a fake server opens another one. To try `tsh ssh` against the fake tsh
# instead, use run-fake (FAKE_TSH_SSH=denied etc. for the error banners).
run-demo: build
	PATH="$(CURDIR)/testdata/fake-tsh:$$PATH" FAKE_TSH_STATE=$(STATE) TELEPORT_HOME=$$(mktemp -d) \
		./bin/$(BINARY) --proxy=127.0.0.1:1 --demo-shell

clean:
	rm -rf bin dist coverage.out
