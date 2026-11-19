# tsh notes

What tstui relies on from `tsh`, and how sure we are of it. Host names are anonymised (`tp.afr.example`).

**Verified** means it was run against tsh **v18.11.3** (go1.26.8) talking to proxies running Teleport **v18.10.0** (community edition, local auth, `second_factor: otp`) on 2026-11-18. **Unverified** means it comes from Teleport's docs or source and has not been run yet. Check each one when it is first used, then move it up.

All commands that tstui runs are in `internal/tsh/cli.go`, and their parsers are in `internal/tsh/parse.go`. Fixtures captured from the real tsh are in `testdata/fake-tsh/`.

## General behaviour (verified)

- Errors go to stderr as `\x1b[31mERROR: \x1b[0m<message>`, coloured even when stderr is not a TTY. `CleanOutput` strips the ANSI codes.
- **JSON on stdout and exit code 1 can happen together** (see `status`). Parse stdout before you look at the exit code.
- `--proxy=HOST[:PORT]` on any command selects the profile for that proxy, so several proxies can be used side by side from one `~/.tsh`. With no matching profile, tsh reports "Not logged in." even if another proxy's profile is active. `TELEPORT_HOME` moves the whole home directory. tstui does not need it, but tests use it for isolation.
- Every subcommand accepts the global flags (`--proxy`, `--user`, `--auth`, `--insecure`, `--mfa-mode`, `--skip-version-check`, `-d`, …).
- Proxy address format is `host:https_port[,ssh_port]`. tstui assumes port **443** when none is given (`tsh.WebAddr`). This is an **unverified** guess at what tsh does with a bare host; recent tsh may probe 443 then 3080.

## `tsh version --client --format=json` (verified)

```json
{"version": "18.11.3", "gitref": "v18.11.3-0-g7ced5182", "runtime": "go1.26.8"}
```

Without `--client` it also contacts the current proxy and adds `"proxyVersion"` and `"proxyPublicAddress"`. tstui uses `--client` (no network). If an older tsh rejects the flag (`unknown long flag '--client'`), tstui retries without it. That fallback is tested against the fake tsh only.

## `GET https://<proxy>/webapi/ping` (verified)

No authentication is needed. This is where the **server version** comes from. Fields tstui reads:

```json
{
  "auth": {"type": "local", "second_factor": "otp", "preferred_local_mfa": "otp",
           "local": {"name": ""}, "default_session_ttl": "12h0m0s"},
  "server_version": "18.10.0",
  "min_client_version": "17.0.0-aa",
  "cluster_name": "afr-prod",
  "edition": "community"
}
```

- `auth.type` is `local`, `oidc`, `saml` or `github`. The default connector name is under `auth.<type>.name`. The connector shapes for oidc, saml and github are **unverified**; only `local` was seen.
- `min_client_version` is the compatibility bound for the version check (`health.CheckVersions`).
- Latency to the proxies from this machine measured 120–520 ms.

## `tsh status --client --format=json [--proxy=X]` (verified)

`--client` reads the local profile without a network round trip. The output is the same as plain `status` for our needs.

| State | stdout | stderr | exit |
|---|---|---|---|
| valid | profile JSON | (empty) | 0 (fake tsh only, since the real cert was expired at spike time) |
| expired | profile JSON | `ERROR: Active profile expired.` | **1** |
| no profile | (empty) | `ERROR: Not logged in.` | 1 |

```json
{
  "active": {
    "profile_url": "https://tp.afr.example:443",
    "username": "ali",
    "cluster": "afr-prod",
    "roles": ["access", "editor"],
    "traits": {"logins": ["ali"], "...": null},
    "logins": ["ali"],
    "kubernetes_enabled": true,
    "valid_until": "2026-11-17T22:14:28+03:30",
    "extensions": ["login-ip", "permit-agent-forwarding", "permit-port-forwarding", "permit-pty", "private-key-policy"]
  },
  "profiles": []
}
```

- `valid_until` is RFC 3339 with the local offset. tstui compares it with the clock and does not trust the exit code.
- `profiles` lists the *other* (inactive) profiles.
- The status JSON has **no MFA field**. Take the MFA type from ping `auth.second_factor` or from the profile's `mfa_mode`.

## Profiles in `~/.tsh` (verified)

- `~/.tsh/<proxy-host>.yaml` holds one profile per proxy, with `web_proxy_addr`, `user`, `cluster`, `mfa_mode`, `tls_routing_enabled` and so on. `tsh.ReadProfiles` parses them.
- `~/.tsh/current-profile` contains the name of the active profile.
- `~/.tsh/keys/<proxy-host>/` holds the keys and certificates. A keys directory can exist **without** a profile yaml (seen on this machine). Profile discovery uses the yaml files only.
- Leaf clusters show up as extra CA files under `keys/<proxy>/cas/`. tstui lists them with `tsh clusters` (below), not from these files.

## `tsh ls --format=json [--proxy=X] [--cluster=C]` (verified)

**Verified 2026-11-18** with tsh v18.11.3 against a v18 root cluster with two leaf clusters: the shape below is what tsh printed (the real output also has `metadata.revision`, `spec.rotation` and `spec.component_features`, which tstui ignores). Earlier errors seen (verified):

- expired cert: `ERROR: ssh: cert has expired`, exit 1
- no profile: `ERROR: Not logged in.`, exit 1

Shape (Teleport `types.ServerV2` list), as in `testdata/fake-tsh/ls.json`:

```json
[{"kind": "node", "version": "v2",
  "metadata": {"name": "<uuid>", "labels": {"env": "prod"}, "expires": "..."},
  "spec": {"addr": "10.0.1.11:3022", "hostname": "web-01", "version": "18.10.0",
           "cmd_labels": {"uptime": {"period": "1m0s", "command": ["uptime"], "result": "..."}}}}]
```

- `spec.addr` is empty for nodes behind a reverse tunnel.
- Command labels are merged into `Server.Labels` using their `result`.
- The output has **no per-node logins**. Allowed logins come from `status.logins`.
- Filtering is available server-side: positional `key=value,...`, `--search=a,b`, and `--query='labels["env"] == "prod"'`.
- `--cluster=C` lists a leaf cluster's nodes through the root proxy, in the same shape (verified). Without it, tsh lists the profile's *selected* cluster, which `tsh login <leaf>` can change, so tstui always passes it once the clusters are known.
- `-R/--all` lists every cluster in one call (verified, 0.75 s for 155 nodes in 3 clusters). Its JSON is a different shape: `[{"proxy": "host:443", "cluster": "<name>", "node": {<ServerV2>}}]`. tstui does not use it: it lists each cluster with `--cluster` instead (about 0.2 s each, four at a time), so a leaf cluster that fails does not hide the others. How `-R` behaves when one leaf is offline is **unverified**.

## `tsh clusters --format=json [--proxy=X]` (verified)

Verified 2026-11-18, tsh v18.11.3, one root and two leaf clusters. Modelled in `testdata/fake-tsh/clusters.json`:

```json
[{"cluster_name": "teleport-ast.example.com", "status": "online", "cluster_type": "root", "labels": null, "selected": true},
 {"cluster_name": "afr.teleport.example.com", "status": "online", "cluster_type": "leaf", "labels": null, "selected": false}]
```

- The root cluster comes first. `selected` marks the profile's selected cluster.
- `status` was `online` for every cluster seen. `offline` is the other value in Teleport's source. tstui skips listing an offline cluster and marks it in the header. That path is **unverified** against a real offline leaf.
- tstui runs it through `tsh.ListAll` (`internal/tsh/clusters.go`). If `tsh clusters` fails with anything but "not logged in", tstui lists the selected cluster alone, as before.
- `tsh ssh`, `tsh scp` and `tsh ssh -N --forward` all take `--cluster=C` (`-c`, verified from `--help`). tstui passes it for every node, in the `--cluster=C` form, so a name can never be read as another flag. Connecting to a leaf node this way is **unverified** on the wire (the flag is from `--help`).

## `tsh login` (wording verified from the binary, flow unverified)

Flags (verified from `tsh login --help`, v18.11.3): `--proxy`, `--user`, `--auth=CONNECTOR`, `--mfa-mode=auto|cross-platform|platform|otp|sso|browser`, `--ttl`, `--browser=none`, `--headless`, `--insecure`, `--request-roles`. There is **no `--force`** (an earlier version of these notes said there was).

tstui runs `tsh login --proxy=X [--user=U] [--auth=C] [--mfa-mode=M] [--insecure]` in a pseudo-terminal with echo turned off (`internal/auth/login.go`), and answers the prompts by typing into it. Secrets are never in argv or the environment.

Prompt format strings, **verified** by reading them out of the v18.11.3 binary (`grep -aoE` on `/usr/local/bin/tsh`); `%s` is empty or `*registered* `:

- password: `Enter password for Teleport user %v`
- OTP only: `Enter an OTP code from a %sdevice`
- WebAuthn and OTP both registered: `Tap any %ssecurity key or enter a code from a %sOTP device`
- WebAuthn only: `Tap any %ssecurity key`; after a touch: `Detected security key tap`
- key PIN: `Enter your security key PIN`
- passwordless user choice: `Choose the user for login`
- SSO: `If browser window does not open automatically, open it by clicking on the link:` then the URL
- success output: `> Profile URL: …`, `Logged in as: %v`; logout prints `Logged out %v from %v.`

**Verified on the wire** (raw PTY output of `tsh login` v18.11.3 against a real v18 proxy, 2026-11-18, nothing typed):

- tsh first writes two terminal queries, `ESC]11;?ESC\` (background colour) and `ESC[6n` (cursor position), **without a newline**. It then waits about **5 s** for the terminal to reply before going on. tstui replies the way a dark terminal would (`internal/auth/login.go`, `terminalQueries`), and the prompt then comes in about 0.1 s.
- The password prompt follows **on the same line**: `Enter password for Teleport user a.soltani:\r\n`, so it ends with `:` and a newline. An early tstui marked the query bytes as a finished partial line and then dropped the rest of that line. The password prompt was never seen, and the login hung until the timeout. This is fixed, and the test `prompt on the same line as earlier output` covers it.
- An unreachable proxy gives `ERROR: Teleport proxy not available at <proxy>.` followed by a tab-indented `Get "https://<proxy>/webapi/ping": dial tcp: lookup <host>: no such host`, and exit 1.

Still **unverified** (all are matched tolerantly, and every pattern can be extended with `tsh_prompts` in the config):

- The line endings of the OTP, tap and PIN prompts. tstui handles both kinds: a line without a newline counts as a prompt once it has been quiet for 150 ms.
- That v18 checks the password **before** asking for the OTP, so a rejection right after typing the OTP means a wrong OTP. tstui relies on this to tell `ErrWrongPassword` from `ErrWrongOTP`.
- Server error texts. These live in the auth server, not the client binary. The expected strings come from Teleport source: `invalid username or password`, `invalid username, password or second factor`, `too many incorrect attempts, please try again later` (lockout), and `lock targeting … is in force` (tctl lock). The wording of an unknown `--auth` connector is not known; tstui matches `connector … not found` and similar.
- Whether tsh re-prompts after a wrong password or exits. tstui handles both: a repeated prompt goes to the popup as a retry.
- `tsh logout --proxy=X` when there is no profile. tstui treats a "not logged in" error as success.

**To verify:** run `tsh login --proxy=<proxy> --user=<user>` by hand once with a wrong password and once with a wrong OTP, then copy the prompts and the `ERROR:` lines here exactly. Or run `tstui --login-only --proxy=<proxy> --log-file=/tmp/l.log`. The log records each prompt kind it recognised, but not the text.

## `tsh ssh` (flags verified, behaviour unverified)

`tsh ssh [--proxy=X] [-l login | login@host] host [command...]`. Useful flags: `-p/--port`, `-L/--forward` (port forwarding), `-N` (no remote command, for forward-only), `-t/--tty`, `-c/--cluster`, `--no-resume`, `--relogin`, `--request-mode=off|resource|role`. `--enable-escape-sequences` is on by default (`~?`).

What the embedded terminal does (`internal/session`). Nothing here has been run against a real proxy yet:

- Command: `tsh ssh --proxy=<proxy> [--insecure] <login>@<hostname>` in a PTY, through `Client.StartPTY`. The hostname comes from `tsh ls`. **Unverified:** that tsh resolves a hostname that several nodes share (it may report it as ambiguous). The node UUID (`metadata.name`) would be unambiguous, if tsh accepts it as the host. `--` is not used before the target: whether tsh's flag parser accepts it is not known. Instead, tstui refuses hosts and logins that start with `-`.
- Escape sequences stay on, so `~.` closes a hung session as in OpenSSH. Typing `~` right after Enter is taken by tsh.
- Environment: `TERM=xterm-256color` and `COLORTERM=truecolor` replace the user's own values, since that is what the emulator implements. tsh passes `TERM` on to the remote PTY. This is verified only with the fake tsh, which execs a local bash.
- tsh's **stderr is a pipe, not the PTY**. tstui shows it in the pane (adding `\r` before `\n`) and keeps the last 8 KiB to explain a failure. With a PTY, the remote program's stderr arrives through the PTY like stdout, so the pipe carries tsh's own messages only. **Unverified:**
  - that tsh behaves the same when its stderr is not a terminal. Per-session MFA prompts ("Tap any security key") are expected on stderr and still show up in the pane.
  - the error texts. They are matched case-insensitively on the last `ERROR:` line (`session.Classify`). `access denied` → access denied. `cert has expired`, `profile expired`, `not logged in` → certificate expired. `mfa`, `second factor` → MFA required. `connection refused`, `no route to host`, `i/o timeout`, `dial tcp`, `proxy not available`, … → unreachable. `not found`, `no matching` → no such server. Anything else is shown as tsh wrote it.
- States: *connecting* until the PTY carries its first byte (tsh writes nothing to stdout before the remote shell), then *connected*. On exit the session is *disconnected* (exit status shown), or *error* when stderr had an `ERROR:` line. The remote shell's exit status is tsh's exit status (verified only with the fake tsh).
- A session is never killed because the certificate expired. **Unverified:** whether the proxy cuts open sessions at expiry (the role option `disconnect_expired_cert`). If it does, the banner shows tsh's error.

**To verify:** open a session to a real node, then run `exit 3` (the banner should say exit 3). Connect as a login your role lacks, and to a stopped node, then copy the `ERROR:` lines here. Try two nodes with the same hostname, and a node that needs per-session MFA.

## Node heartbeat in `tsh ls` (shape modelled, behaviour unverified)

`metadata.expires` (RFC 3339) is parsed into `Server.Expires`, and the reachability dot uses it (`health.NodeStatus`). A node whose heartbeat had already run out when listed shows `○`. The assumption is that Teleport lists a node only while its heartbeat is current and that nodes renew it every few minutes. Then `○` should be rare, and `·` means tsh gave no expiry. **To verify:** confirm that real `tsh ls --format=json` output carries `metadata.expires` for every node, then stop a node and watch how long it stays listed.

## Client/server version rule (documented)

From https://goteleport.com/docs/upgrading/overview/ ("Component compatibility", read 2026-11-18): "Servers support clients that are one major version behind, but do not support clients that are on a newer major version." So a v18 proxy supports tsh v18 and v17, which matches the verified ping above (`server_version` 18.10.0, `min_client_version` 17.0.0-aa). The page gives no minor or patch rule. tstui applies the major-version rule plus `min_client_version` in `health.CheckVersions`, the only place the rule lives.

## `tsh ssh` without a terminal (flags verified, behaviour unverified)

Multi-exec (`internal/multiexec`) runs `tsh ssh [--proxy=X] [--insecure] login@host '<command>'` through `Client.Exec`: no PTY, empty stdin, stdout and stderr captured separately (256 KiB each), and the process group killed on timeout or cancel. The command is **one** argv element.

Unverified:

- That tsh joins the command arguments and sends them as one exec request. The remote login shell should then interpret the string as OpenSSH does. With a single argument there is nothing to join.
- How tsh's flag parser treats a command that starts with `-`. tstui refuses such commands, so this never comes up.
- That the remote command's exit status becomes tsh's exit status without a TTY. With the PTY it is (fake tsh only, see above).
- What happens when a node needs per-session MFA and there is no terminal. tsh probably fails with an MFA error, which tstui shows as that host's error. Running multi-exec against such nodes would need a PTY per host.

## `tsh scp` (flags verified, behaviour unverified)

Flags verified from `tsh scp --help` (v18.11.3): `usage: tsh scp [<flags>] <from, to>...`, `-r/--[no-]recursive`, `-p/--[no-]preserve`, `-q/--[no-]quiet`, `-P/--port`, `-c/--cluster`, plus the global flags. tstui runs `tsh scp [--proxy=X] [--cluster=C] [--insecure] [--recursive] SRC DST`, with the remote side written as `login@host:path`, through `Client.Exec` (no PTY). The local path is always made absolute first, so it never starts with `-`.

Unverified:

- How tsh decides that an argument is remote. tstui assumes that any argument containing `:` is, and therefore refuses local paths that contain `:`. If tsh treats absolute paths as local regardless (OpenSSH treats anything with a `/` before the first `:` as local), that restriction can be relaxed.
- That `login@host:` (empty path) means the home directory.
- Whether the remote path is glob-expanded. Since v14, tsh scp uses SFTP, which may not run a remote shell.
- Progress output without a TTY. tstui shows a spinner and the elapsed time, not tsh's progress bar.
- Error texts, for example `no such file or directory` and `permission denied`. tstui shows the last `ERROR:` line as is.

## Port forwarding: `tsh ssh -N -L` (flags verified, behaviour unverified)

Flags verified from `tsh ssh --help` (v18.11.3): `-L, --forward=FORWARD ...` ("Forward localhost connections to remote server") and `-N, --[no-]no-remote-exec` ("Don't execute remote command, useful for port forwarding"). `-D` (SOCKS) and `-R` exist too, and tstui does not use them. `tsh proxy ssh` is a ProxyCommand helper for OpenSSH. `tsh proxy app` and `tsh proxy db` reach only registered apps and databases. That leaves `tsh ssh -N -L` as the way to forward to any host:port behind a node.

tstui runs `tsh ssh [--proxy=X] [--insecure] -N --forward=BIND:LPORT:HOST:RPORT login@node` in a PTY (`Client.StartPTY`), drains its output, and reports it as active once BIND:LPORT can no longer be bound. It never connects to the port to check.

Unverified:

- That tsh accepts the four-part `bind:port:host:port` form in one `--forward=` value. The three-part form is what the docs show.
- Whether tsh prints anything once the listener is up, which would give a better ready signal than probing the port.
- The bind error when the port is taken after tstui's check, presumably `bind: address already in use`. It is shown as the forward's error.
- Per-session MFA: tsh should prompt on the PTY. tstui shows the last line tsh printed in the forward's INFO column, so "Tap any security key" is visible, but an OTP cannot be typed there.

## Version differences

Only v18.11.3 has been seen. Known risk points:

- `--client` on `status` and `version` (fallback exists).
- Login prompt wording. The v18.11.3 strings are listed above, and the config key `tsh_prompts` adds patterns for other versions.
- The `tsh ls` JSON shape is stable across v15–v18 according to the docs (unverified).
