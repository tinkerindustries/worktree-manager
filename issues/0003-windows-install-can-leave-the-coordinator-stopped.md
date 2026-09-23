# The Windows installer can leave the coordinator stopped

- **Status:** open
- **Found:** 2026-09-24, on Windows 11, upgrading 0.12.0-dev.4+g2f0c854 to 0.12.0-dev.10+gf938871 with `install.ps1 -Addr 127.0.0.1:7833`
- **Component:** `dist/install.ps1`, `internal/platform/task_windows.go`
- **Severity:** every `wt` command fails with exit 5 until someone starts the task by hand; nothing restarts it

## What happens

Two failures, on two consecutive runs of the same installer. Both happen
after `install.ps1` has run `schtasks /End` on the coordinator's task, so
both leave the machine with no coordinator running.

**First run: replacing `wtd.exe` fails.**

```
replacing wt 0.12.0-dev.4+g2f0c854 (2f0c854) with 0.12.0-dev.10+gf938871 (f938871)
Move-Item: ...\install.ps1:323
 323 |      Move-Item -Force $tmp (Join-Path $BinDir $bin)
     | Cannot create a file when that file already exists.
```

`wt.exe` was replaced; `wtd.exe` was not, and `.wtd.exe.tmp` was left
beside it. A few seconds later no `wtd` process was running, and the
same installer run again got past this step. The likely cause is that
`schtasks /End` returns before the process has exited and released its
image, so the rename meets a file still in use. Not confirmed.

**Second run: re-registering the task fails.**

```
registering the coordinator's logon task: schtasks /Create /F /TN com.mrgeoffrich.wtd /XML C:\Users\<user>\AppData\Local\wt\com.mrgeoffrich.wtd.xml: exit status 1: ERROR: Access is denied.
```

The existing task had been registered on 2026-09-05. Why a non-elevated
`/Create /F` cannot overwrite it is unknown; one candidate is that it was
first registered from an elevated shell. `schtasks /Query` of the task
works, and `schtasks /Run` of it succeeded and started the new `wtd.exe`,
because the existing task already names the same path and `--addr`.

## How to reproduce

1. On Windows, with a coordinator installed and running, build an archive
   (`dist/build.sh`), unpack the windows zip and run `install.ps1`.
2. The first failure is a race and may not reproduce every time.
3. The second needs a task this user cannot overwrite; how the task got
   that way here is unknown.

## What would fix it

- Wait for the `wtd` process to exit after `schtasks /End` before the
  rename, with a bounded retry.
- When `/Create` fails, still `/Run` the task that is already registered
  if it names the same binary path, and say that the registration was not
  refreshed, rather than exiting with the coordinator stopped.
- More generally, no failure after `/End` should exit without trying to
  start the coordinator again.

## Workaround

`schtasks /Run /TN com.mrgeoffrich.wtd`, then `wt daemon status`.
