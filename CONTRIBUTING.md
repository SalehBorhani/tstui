# Contributing

Thanks for helping. This page covers building, testing and the conventions the code follows. [CLAUDE.md](CLAUDE.md) lists the rules in full, and [docs/architecture.md](docs/architecture.md) explains how the pieces fit together.

## Setup

- Go 1.27.1 or newer (`go.mod`). Nothing else is needed to build: dependencies are vendored.
- For `make lint`: [golangci-lint](https://golangci-lint.run/) v2.
- For `make race`: a C compiler, because the race detector needs cgo.
- For `make release`: `dpkg-deb` for the `.deb` files (on Debian/Ubuntu it comes with `dpkg`). Without it, only the tarballs are built.
- tsh is **not** needed for development. Tests use a fake.

## Commands

The `Makefile` is the source of truth.

| Command | Does |
|---|---|
| `make build` | `bin/tstui`, static, from `vendor/`, version from git |
| `make test` | all tests |
| `make race` | all tests with the race detector |
| `make vet`, `make lint` | `go vet`, golangci-lint (`.golangci.yml`) |
| `make check-static` | builds and checks with `ldd` that the binary has no dynamic dependencies |
| `make offline-build` | builds with no network and empty Go caches (Linux: in a network namespace) |
| `make release` | `dist/`: plain binaries and `.tar.gz` for linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64, `.deb` for Linux, `SHA256SUMS` |
| `make run-fake STATE=expired` | runs tstui against the fake tsh (`valid`, `expiring`, `expired`, `logged-out`, `denied`) |
| `make run-demo` | the same, with a local bash in place of SSH |

Before sending a change, run:

```sh
make vet test lint build race
```

## Tests

- **Table-driven** wherever there is more than one case.
- **No real tsh.** Use `tsh.Fake` (in memory) or the fake tsh script `testdata/fake-tsh/tsh`, which is controlled by `FAKE_TSH_STATE` and the other variables in its header. A test that runs the bare name `tsh` must also set `t.Setenv("PATH", …)`: `exec` looks the binary up in the test process's `PATH`, not the child's environment, so without it the real tsh runs.
- **No leaked goroutines.** `session`, `vterm` and `ui` run goleak in `TestMain`. Every goroutine must end when its session or the app closes.
- **UI tests** use tcell's `SimulationScreen`. Sessions in tests use `session.Fake` or `/bin/sh` in a real PTY.
- **Secrets.** Any new code path that handles a password, OTP or PIN needs a test that captures the log or the child's argv and environment and checks that the secret is absent (see `TestCLINoSecretsReachChild`).
- **Examples.** `examples/config.yaml` must show every config key, and every file in `examples/skins/` must load. Tests check both, so when you add a config key, add it to the example.

## Conventions

- Small packages. Pure logic (`filter`, `palette`, `layout`) has no I/O and no tview.
- Errors are wrapped with context (`fmt.Errorf("load config: %w", err)`). No global state.
- Every tsh call goes through `tsh.Client`. Interactive commands use `StartPTY`, the others `Exec`. Arguments are always an argv: never `sh -c`, and no user string where tsh could read it as a flag.
- The UI never blocks. Run tsh in a goroutine (`App.goSafe`) and update the UI with `App.post`. Stop the app with `App.shutdown`, never `tv.Stop`.
- A wrapper primitive (like `modalLayout`) must forward `InputHandler`, because tview v0.42 hands keys down from the root.
- Stay on tagged tview releases.
- Before touching anything that runs tsh, read [docs/tsh-notes.md](docs/tsh-notes.md). When you verify an assumption against a real tsh, move it to the verified part of that file.
- User-facing features are documented in [docs/features.md](docs/features.md), and new keys go into the help overlay and the footer hints.

## Dependencies

After adding or upgrading a module:

```sh
make vendor          # go mod tidy && go mod vendor
make offline-build   # proves the vendored tree is complete
```

Commit `vendor/` with the change. The terminal emulator `charmbracelet/x/vt` is patched locally in `third_party/charmbracelet-x-vt`. Read `docs/terminal-decision.md` and its `PATCHES.md` before upgrading it.

## Releases

1. Update `CHANGELOG.md`: move the items under a new `## [x.y.z] - date` heading.
2. Tag: `git tag -a v0.1.0 -m "tstui 0.1.0" && git push --tags`.
3. CI (`.github/workflows/ci.yml`) runs the checks, builds the artifacts with `make release`, and publishes a GitHub release with every file in `dist/` attached and that version's `CHANGELOG.md` section as the notes.

To build the same artifacts by hand, run `make release`. They land in `dist/`.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE), as the rest of the project. A new dependency must have a license compatible with it. `make release` copies every vendored module's license into `THIRD_PARTY_LICENSES`.
