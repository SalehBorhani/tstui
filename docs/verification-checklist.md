# Verification checklist

Manual tests against real Teleport clusters before a release. The automated tests use a fake tsh, so this list covers what only a real proxy, a real node and a real security key can show. Several items also check an **unverified** assumption in [tsh-notes.md](tsh-notes.md). When one passes, move that note to the verified part of the file.

Record the environment at the top of each run:

```
date:          ____-__-__
tstui:         tstui --version       → ____________
tsh:           tsh version           → ____________
proxies:       ____________ (Teleport v__)   ____________ (Teleport v__)
leaf clusters: ____________
OS / terminal: ____________ / ____________ (size ___x___)
```

Use a debug log for every run, and keep it if something fails. It contains no secrets.

```sh
tstui --log-file /tmp/tstui-check.log
```

**Before you start**

- [ ] `tstui --report --proxy <proxy>` shows the tsh version, the server version, the auth type, the MFA type and the latency, with no version warning.
- [ ] `ls -l ~/.config/tstui/config.yaml` shows `-rw-------`. After `chmod 644`, tstui warns at start. Set it back with `chmod 600`.

## 1. Login

Log out between items: `tsh logout --proxy=<proxy>`, or `o` in tstui.

### 1.1 Password only (a proxy with `second_factor: off`, or a user without MFA)

- [ ] `tstui --proxy <proxy>` opens the login popup with the proxy filled in and the cursor on User.
- [ ] Type the user and password and press `enter`. A spinner shows, then the server list. The header shows `✔ logged in`, the user, the roles and a countdown.
- [ ] `echo -n "$PW" | tstui --proxy <proxy> --user <user> --password-stdin` logs in without showing the popup.
- [ ] `ps -ef | grep tsh` during the login shows no password in any argv.

### 1.2 Password + OTP

- [ ] The popup asks for the password, then the **MFA code**. A valid code logs in.
- [ ] `--mfa-token <code>` with `--password-stdin` logs in with no popup at all.
- [ ] `TSTUI_PASSWORD=… TSTUI_MFA=… tstui --proxy <proxy> --user <user>` logs in. `cat /proc/$(pgrep -n tsh)/environ | tr '\0' '\n' | grep TSTUI` shows nothing.

### 1.3 WebAuthn / security key

- [ ] With only a key registered: the popup shows "touch your security key". A touch shows "Security key touched, logging in…", then the list.
- [ ] With both a key and OTP registered: the popup offers the code field and asks for a touch. Both ways work. A touch withdraws the code field.
- [ ] A key with a PIN: the popup asks for "Key PIN:", masked.
- [ ] Not touching the key: after 3 minutes the popup says "login timed out". `l` starts a new login.
- [ ] **Record** the exact prompt lines from `tsh login --proxy=<proxy>` run by hand, in tsh-notes ("line endings of the OTP, tap and PIN prompts").

### 1.4 SSO (if a connector exists)

- [ ] `tstui --proxy <proxy> --auth <connector>`: the popup shows the browser URL, the browser opens or the link works, and the list appears after the browser login.

### 1.5 Wrong credentials

- [ ] Wrong password: the popup says **wrong user name or password** and lets you retry without restarting.
- [ ] Right password, wrong OTP: the popup says **wrong OTP code**. If it says "wrong credentials" or "wrong user name or password" instead, record that in tsh-notes: the assumption that v18 checks the password before the OTP is wrong.
- [ ] Wrong user name: the same message as a wrong password.
- [ ] `--auth nosuchconnector`: **auth connector not found**. **Record** tsh's `ERROR:` line.
- [ ] (Test user only) Enough failed attempts to lock the user: **user is locked**. **Record** tsh's `ERROR:` line.
- [ ] Unreachable proxy (`--proxy nosuch.example.com`): **cannot reach the proxy** within the network timeout, and the UI stays responsive.
- [ ] `Cancel` or `esc` in the popup stops the login. `ps` shows no leftover `tsh login`.

## 2. Proxies and clusters

- [ ] `p` lists every configured proxy and every `~/.tsh` profile with state (logged in / expired / no session) and latency.
- [ ] `enter` on a second proxy switches: the header, clusters and server list change, and a login popup appears if needed.
- [ ] A proxy's shortcut letter switches directly. `:proxy <name>` does too, and `tab` completes the name.
- [ ] Switching back keeps the first proxy's filter and selection.
- [ ] The clusters panel lists the root and leaf clusters with server counts. `0` shows all of them (with a CLUSTER column), `1`-`9` one each.
- [ ] Connecting to a node in a **leaf** cluster works (`--cluster` is passed). This is unverified in tsh-notes.
- [ ] A node hostname that exists in two clusters connects to the one selected.
- [ ] Disconnect the network, then press `r`: the list shows **STALE** with its age, and the header shows the proxy unreachable. Reconnect and press `r`: the list is live again.

## 3. Sessions

### 3.1 Two sessions

- [ ] `enter` on server A opens tab 1 with a shell. `ctrl-b tab`, then `enter` on server B, opens tab 2.
- [ ] `alt-1` / `alt-2` and `ctrl-b 1` / `ctrl-b 2` switch tabs. Output in the hidden tab marks it with `*`.
- [ ] `enter` on server A again focuses tab 1 instead of opening a third tab. `n` opens a third.
- [ ] `ctrl-b |` with server B picked splits tab 1. Arrows and `ctrl-b o` move between panes. `ctrl-b z` zooms and `ctrl-b f` goes fullscreen.
- [ ] In the remote shell, `exit 3`: the banner says the session closed with exit 3. `r` reconnects and `c` closes. (tsh-notes: exit status passthrough.)
- [ ] `ctrl-c` and `ctrl-d` reach the remote shell. `ctrl-b ctrl-b` sends a literal `ctrl-b` (check with `cat -v`).
- [ ] A login your role lacks shows **access denied**. A stopped node shows **cannot be reached**. **Record** both `ERROR:` lines in tsh-notes.
- [ ] A node with per-session MFA: the pane asks for a touch, and the session opens after it.

### 3.2 Full-screen programs

- [ ] `vim`: open a file, `:set number`, insert text with arrows, `:wq`. Colours and the cursor are right, and the screen is restored after exit.
- [ ] `htop`: colours, meters and function keys (`F6` sort, `F10` quit) work. The mouse clicks a column header.
- [ ] `less /var/log/syslog`: `pgup`/`pgdn`, `/search`, `q`. The alternate screen is restored.
- [ ] `tmux` on the node: the prefix `ctrl-b` must be sent twice (`ctrl-b ctrl-b`). Borders are drawn as lines, not `qqq`.
- [ ] `mc` if installed: borders, function keys and mouse.
- [ ] UTF-8: `echo 日本語 é 👍` aligns, and the cursor lands right after it.
- [ ] Scrollback: `seq 20000`, then `ctrl-b [` and `pgup` reaches early lines, and `q` leaves. The mouse wheel scrolls too.
- [ ] Throughput: `yes | head -2000000`, or `cat` a big file, finishes in seconds. `ctrl-c` stops it promptly.

### 3.3 Resize

- [ ] Resize the host terminal while `htop` runs: htop redraws at the new size. `stty size` in the shell shows the pane's size.
- [ ] Shrink below 80x24, then to about 40x12: nothing panics, and panes and the header degrade.
- [ ] Split panes and resize: each pane's `stty size` matches its rectangle.
- [ ] `F` (fullscreen) and back: the remote program redraws at the new size each time.

## 4. Certificate expiry

Use a short TTL: `tsh login --proxy=<proxy> --ttl=35` (minutes), then start tstui.

- [ ] The countdown turns **yellow** under 30 minutes, and a notice appears for 20 s.
- [ ] Under 5 minutes it turns **red**, and the notice stays.
- [ ] At expiry the header shows expired. Open sessions keep running. Record whether the proxy cuts them (`disconnect_expired_cert`), as tsh-notes asks.
- [ ] `L` opens the login popup. After logging in, the countdown resets and the list refreshes. Sessions opened before are untouched.
- [ ] Opening a new session while expired shows **certificate has expired**.

## 5. File transfer (scp)

- [ ] `u` on a server: upload a small file to an empty remote path (the home directory), with completion in the local path field. It succeeds, and the file is there (check with `ls` in a session).
- [ ] Upload a directory: recursive turns on, and the tree arrives.
- [ ] `d`: download a remote file to `~/Downloads/`. The content matches (`sha256sum` on both sides).
- [ ] A local path with a space, and a remote path with a space: both work. **Record** whether the remote path is glob-expanded (`*.log`).
- [ ] A missing remote file: tsh's error shows in the dialog.
- [ ] A large file (about 500 MB): the spinner and elapsed time show, and `Cancel` stops it with no `tsh scp` left in `ps`.
- [ ] `~/.local/state/tstui/transfers.log` has one line per transfer, mode 0600.
- [ ] A leaf-cluster node works too.

## 6. Port forwards

- [ ] `:pf`, then `n`: forward `15432:localhost:5432` (or any service on the node). The state goes `starting` → `active`, and `nc -z 127.0.0.1 15432` connects. **Record** whether the 4-part `bind:port:host:port` form works.
- [ ] A forward on a port already in use: tstui refuses it before starting tsh.
- [ ] `s` stops it: the port is free again (`ss -ltn`).
- [ ] `f` saves it to the config. After a restart, `enter` on it starts it.
- [ ] Quitting with a forward running asks first. After quitting, `ps` shows no `tsh ssh -N`.
- [ ] A node with per-session MFA: the INFO column shows "Tap any security key", and a touch makes it active.

## 7. Multi-exec

- [ ] Mark 3 servers with `space`, then `:exec uptime`. The confirmation lists the 3 hosts, the login, the concurrency and the timeout. The results show each host's exit 0 and output.
- [ ] `:exec 'exit 2'`, or a command that fails on one host: that host shows exit 2, and stderr is shown. (tsh-notes: exit status without a TTY.)
- [ ] `:exec sleep 120` with `exec.timeout: 10s`: each host is killed after 10 s with a timeout status, and `x` cancels a run early.
- [ ] `a` marks every shown server. A run on more servers than `exec.concurrency` keeps at most that many `tsh ssh` processes alive (check with `ps`).
- [ ] `:exec sudo reboot` (**cancel it!**): the frame is red, the reasons are listed, and it needs `yes` typed. Answer anything else, or `esc`: nothing runs.
- [ ] `e` exports the results. The file is mode 0600 and has every host's output.
- [ ] `~/.local/state/tstui/audit.log` has a `start` and a `finish` line for each run, with no output and no secrets.
- [ ] A node needing per-session MFA: record what tsh reports without a TTY.

## 8. Shutdown and robustness

- [ ] `q` with sessions open asks first. After quitting, the terminal is normal (no raw mode, cursor visible), and `ps` shows no tsh children.
- [ ] `kill -TERM <tstui pid>` with sessions open: tstui exits with 143, the terminal is restored and no tsh is left.
- [ ] Close the terminal window (SIGHUP) with sessions open: no tsh is left (`ps` from another terminal).
- [ ] Rename tsh out of `PATH` and start tstui: the message says how to install tsh.
- [ ] `/tmp/tstui-check.log`: `grep -i` for your password and OTP finds nothing.

## Results

| Section | Pass | Notes / tsh-notes updated |
|---|---|---|
| 1 Login | | |
| 2 Proxies and clusters | | |
| 3 Sessions | | |
| 4 Expiry | | |
| 5 scp | | |
| 6 Port forwards | | |
| 7 Multi-exec | | |
| 8 Shutdown | | |
