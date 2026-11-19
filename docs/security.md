# Security

tstui handles Teleport passwords and one-time codes, and it runs commands on many servers. This page explains how secrets move through it, what it writes to disk, and which tests enforce the rules. To report a vulnerability, contact the maintainers privately rather than opening a public issue.

## What tstui trusts

- **tsh** is trusted. tstui runs the first `tsh` on `PATH`, so whoever controls your `PATH` controls what receives your password. This is the same trust you give tsh when you type `tsh login`.
- **The Teleport proxy and cluster** are trusted the way tsh trusts them. tstui verifies the proxy's TLS certificate for its own `/webapi/ping` request unless you pass `--insecure`.
- **Remote servers** are not trusted. Their output is drawn by tstui's terminal emulator and never reaches your real terminal as raw escape sequences. The emulator answers status queries (cursor position, colours, device attributes) and has no clipboard handler, so a remote program cannot write your clipboard (OSC 52). tstui writes the clipboard only when you select text with the mouse.
- **What you type** in dialogs (paths, hosts, commands, forward specs) is passed to tsh as separate argv elements, never interpreted by a local shell.

## Credentials

**Where they come from.** Flags (`--password-stdin`, `--password-file`, `--password`, `--mfa-token`), environment variables (`TSTUI_PASSWORD`, `TSTUI_MFA`), or the login popup. The config file has no field for a password or OTP.

**How they are held.** Every password, OTP code and key PIN lives in an `auth.Secret`, a `[]byte` that:

- prints as `[REDACTED]` through `String`, `GoString`, every `fmt` verb, and `slog`
- is overwritten with zeros by `Wipe()` as soon as it has been typed into tsh, when a login ends, and when the app exits

A Go `string` cannot be wiped, so secrets are never converted to strings. The popup's input fields are the exception: tview keeps their text as a string. Each field is cleared the moment its value is moved into a `Secret`, which drops tstui's reference, but the string stays in memory until the garbage collector reuses it.

**How they reach tsh.** Only by typing into the `tsh login` pseudo-terminal when tsh prompts, with echo turned off. Never through:

- **argv**: visible to every user through `ps`
- **the environment**: `tsh.SafeEnv` removes `TSTUI_PASSWORD` and `TSTUI_MFA` from every child process, tsh and the remote session included
- **files**

**Warnings.** `--password` works but warns that the value shows in shell history and `ps`. `--password-file` warns when the file is readable by group or others. Values read from stdin or a file are limited to 4 KiB.

## Logs and error messages

The debug log (`--log-file`, mode 0600) records each tsh command through `tsh.RedactArgs` (which masks the values of secret-looking flags), its exit code and its duration. It never records tsh's stdout or stderr, terminal contents, or prompt text: login logs only the *kind* of prompt it recognised.

Error messages show tsh's stderr, for example `ERROR: access denied`. That is safe because no secret is ever passed to tsh in a form it could echo.

## Crashes and core dumps

- At start, tstui sets its soft core-file limit to 0. The limit is inherited by tsh and by the sessions it starts, so a crash cannot write memory, and the secrets in it, to disk.
- A panic is recovered. tstui closes sessions, wipes credentials and restores the terminal, then prints the panic value and a stack trace. A `Secret` in the panic value prints as `[REDACTED]`, and the stack holds code locations, not memory contents.
- SIGINT, SIGTERM and SIGHUP shut down the same way: child processes (sessions, port forwards, transfers) are killed by process group, so none outlive tstui.

## Files

| File | Mode | Contents | Secrets |
|---|---|---|---|
| `~/.config/tstui/config.yaml` | 0600, written atomically | proxies, users, connector names, favorites, saved forwards | none |
| `~/.config/tstui/config.yaml.bak` | 0600 | the previous config | none |
| `~/.local/state/tstui/logins.json` | 0600 | SSH login per server | none |
| `~/.local/state/tstui/servers/*.json` | 0600 | last server list per proxy (hosts, addresses, labels) | none |
| `~/.local/state/tstui/transfers.log` | 0600 | time, direction, paths, result | none |
| `~/.local/state/tstui/audit.log` | 0600 | multi-exec: time, proxy, login, hosts, command, results | none; output not recorded |
| exec export (`e` in results) | 0600 | multi-exec output | whatever the remote command printed |

Directories are created with mode 0700. `config.Save` always writes with mode 0600, and `config.Load` warns when the file is more open than that. tsh's own certificates stay in `~/.tsh`, managed by tsh.

A multi-exec command is written to the audit log as typed. Do not put secrets in a multi-exec command line: they would land in the audit log, the remote shell history and the remote process list.

## Running commands on servers

- **Multi-exec** asks for confirmation with the full host list. For a risky command (recursive `rm`, `shutdown`/`reboot`, `mkfs`, `dd`, writes to a disk device, recursive `chmod`/`chown` on `/`, `kill -1`, fork bombs), you must type `yes`. The run is written to the audit log *before* it starts. If the log cannot be written, the run is refused.
- **File transfer** makes local paths absolute (so none starts with `-`) and refuses local paths that contain `:`, which tsh would read as a remote path.
- **Port forwards** bind to `127.0.0.1` unless you give an address. Binding to `0.0.0.0` exposes the forwarded service to your network, as with `ssh -L`.
- Hosts and logins starting with `-`, and commands starting with `-`, are refused, so no user value can be read as a tsh flag.

## Tests that enforce this

| Test | Checks |
|---|---|
| `auth.TestSecretNeverPrints` | every formatting path prints `[REDACTED]` |
| `auth.TestSecretWipe` | `Wipe` zeroes the bytes |
| `tsh.TestCLINoSecretsReachChild` | secret env vars and values never reach a child's argv or environment |
| `tsh.TestStartCommandPTYStripsSecrets` | the same for PTY children |
| `tsh.TestExecRunnerLogsNoSecrets`, `tsh.TestRedactArgs` | logged commands are redacted |
| `cmd/tstui.TestNoSecretsInCapturedLog` | a full login with the password from every source leaves no trace in the captured log |
| `ui` panic tests (`panic_test.go`) | a crash restores the terminal and wipes credentials |
| `config` tests | saved files are 0600, loose permissions warn |

Any new code that handles a secret must come with a test that captures the log or the child's argv and checks that the secret is absent.
