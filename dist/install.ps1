# wt — the worktree manager installer (Windows).
#
# Installs the two binaries from this archive into a prefix and registers
# the coordinator with the Task Scheduler as the logon task, starting it —
# the same registration `wt daemon install` performs, driven by the
# installer rather than reimplemented (docs/ARCHITECTURE.md §13.1).
#
# An explicit -Prefix is a self-contained install: the binaries land in
# <prefix>\bin and the task XML is written under the same prefix without
# registering anything (the `wt daemon install --prefix` rail). Without
# -Prefix the binaries go to %LOCALAPPDATA%\wt\bin and the logon task is
# registered for real.
#
# The coordinator listens on a loopback HTTP port. -Addr overrides the
# default; -ContainerToken admits container clients (16+ characters). The
# two are independent: a custom port needs no token, and a token needs no
# custom port. -AllowHost names Host header values the coordinator accepts
# beyond loopback and its own address, which is what a container reaching
# the host by name — host.docker.internal on Docker Desktop — needs to get
# past the DNS-rebinding guard.
#
# Before anything is copied the two binaries are verified against the
# SHA256SUMS manifest this archive carries (build.sh writes it) — an
# archive without one is refused, not skipped; -SkipVerify is the
# deliberate override. A verification happens before an upgrade too, and
# the version line says what is being replaced and with what.
#
# -DryRun prints every action — the verification, the paths that would be
# written, the address and container-token decision, the supervisor
# command — and changes nothing on disk; the last line is "dry run:
# nothing was changed".
#
# -Uninstall reverses the install: it drives `wt daemon uninstall` (which
# stops the coordinator, deregisters the logon task and removes the task
# XML) and then removes the two binaries. The store is never removed — it
# is the only record of what is allocated on the machine — and a registry
# that still holds entries makes the verb (and so this script) refuse with
# exit 3, naming `wt list` and `wt rm`; `wt daemon uninstall --force` is
# the documented way past that refusal.
#
# -ClientOnly installs the wt client alone: wtd.exe is not copied and no
# logon task is registered. That is the container install — a container
# runs the client and reaches a coordinator that lives on the host, so a
# wtd inside the image would be a second coordinator with its own store,
# which is precisely what must not happen. It composes with -Prefix and
# -DryRun, and it refuses alongside -Addr and -ContainerToken, which
# configure a coordinator this install does not have.
param(
    [string]$Prefix = "",
    [string]$Addr = "",
    [string]$ContainerToken = $env:WT_CONTAINER_TOKEN,
    [string[]]$AllowHost = @(),
    [switch]$ClientOnly,
    [switch]$SkipVerify,
    [switch]$DryRun,
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"

function usage {
    @"
usage: .\install.ps1 [-Prefix <dir>] [-Addr <addr>] [-ContainerToken <tok>]
                     [-AllowHost <host>[,<host>]] [-ClientOnly] [-SkipVerify]
                     [-DryRun] [-Uninstall]

  -Prefix <dir>          install the binaries into <dir>\bin and write the
                         task XML under <dir> (self-contained; nothing is
                         registered). Default: %%LOCALAPPDATA%%\wt\bin
                         with a real logon-task registration.
  -Addr <addr>           the loopback address the coordinator listens on.
                         Default: a free port, chosen at install time.
  -ContainerToken <tok>  admit container clients with this token (at least
                         16 characters; WT_CONTAINER_TOKEN also works,
                         which keeps it out of shell history). Absent,
                         only host clients are admitted.
  -AllowHost <host>      Host header values the coordinator accepts beyond
                         loopback and its own address, e.g.
                         host.docker.internal (comma-separated for more
                         than one). A container reaching the host by name
                         needs its name here, or the coordinator's
                         DNS-rebinding guard refuses the request.
  -ClientOnly            install the wt client alone — no wtd.exe, no
                         logon task. The container install: the client
                         reaches a coordinator on the host through
                         WT_ENDPOINT and WT_CLIENT_TOKEN. Refuses
                         alongside -Addr and -ContainerToken, which
                         configure a coordinator this install has not got.
  -SkipVerify            install without checking the binaries against the
                         archive's SHA256SUMS (deliberate override; the
                         check runs by default and refuses on a mismatch
                         or a missing manifest).
  -DryRun                print every action — verification, paths, the
                         address and container-token decision, the
                         supervisor command — and change nothing on disk.
  -Uninstall             the reverse: drive `wt daemon uninstall` (stop
                         the coordinator, remove the registration), then
                         remove the two binaries. The store is never
                         removed.
"@
}

if ($Prefix -eq "") {
    $Prefix = Join-Path $env:LOCALAPPDATA "wt"
    $RegPrefix = ""
} else {
    $RegPrefix = $Prefix
}

# -ClientOnly installs no coordinator, so the two flags that configure one
# have nothing to configure. Refusing is the house rule — a flag that is
# silently dropped is worse than one that is rejected, because the operator
# believes the container was given a token it never got.
if ($ClientOnly) {
    if ($Addr -ne "") {
        Write-Error "-Addr configures the coordinator, which -ClientOnly does not install; set WT_ENDPOINT in the container instead"
    }
    if ($ContainerToken -ne "" -and $null -ne $ContainerToken) {
        Write-Error "-ContainerToken configures the coordinator, which -ClientOnly does not install; set WT_CLIENT_TOKEN in the container instead"
    }
    if ($AllowHost.Count -gt 0) {
        Write-Error "-AllowHost configures the coordinator, which -ClientOnly does not install; pass it where the coordinator is installed"
    }
}

# $Bins is what this install handles: both binaries normally, the client
# alone under -ClientOnly. Every step below — the presence check, the
# verification, the copy, the removal — reads it, so the two shapes cannot
# drift apart.
$Bins = if ($ClientOnly) { @("wt.exe") } else { @("wt.exe", "wtd.exe") }

# The archive's own directory: the two binaries must be right next to this
# script, which is how the distribution is assembled. An uninstall does not
# need them — it drives the already-installed wt — so the presence check is
# the install path's.
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
if (-not $Uninstall) {
    foreach ($bin in $Bins) {
        if (-not (Test-Path (Join-Path $ScriptDir $bin))) {
            Write-Error "$bin is missing from this distribution (install from the archive, not from a bare copy)"
        }
    }
}

$BinDir = Join-Path $Prefix "bin"

# The store path, as the verb reports it: WT_HOME when set, else $HOME/.wt.
function Get-StorePath {
    if ($env:WT_HOME) { return $env:WT_HOME }
    return Join-Path $env:USERPROFILE ".wt"
}

# Get-WtIdentity prints a wt binary's identity as "VERSION (COMMIT)" — the
# parenthesised tail of its `wt --version` line — or "" when the binary is
# missing or its version line is unreadable. That is what lets the
# installer say what is being replaced and with what.
function Get-WtIdentity([string]$Path) {
    if (-not (Test-Path $Path)) { return "" }
    $line = & $Path --version 2>$null
    if ($LASTEXITCODE -ne 0) { return "" }
    # "wt 0.2.0 (a8e3192)"
    if ($line -match '^wt\s+(\S+)\s+\((\S+)\)') {
        return "$($matches[1]) ($($matches[2]))"
    }
    return ""
}

function Write-VersionLine {
    $new = Get-WtIdentity (Join-Path $ScriptDir "wt.exe")
    if (-not $new) { $new = "version unknown" }
    $oldWt = Join-Path $BinDir "wt.exe"
    if (Test-Path $oldWt) {
        $old = Get-WtIdentity $oldWt
        if ($old) {
            Write-Host "replacing wt $old with $new"
        } else {
            Write-Host "replacing wt (installed version unreadable) with $new"
        }
    } else {
        Write-Host "installing wt $new"
    }
}

# Test-Binaries checks the two binaries against the archive's own
# SHA256SUMS (the manifest build.sh writes inside every archive, covering
# exactly wt.exe and wtd.exe). A missing manifest is a refusal too — an
# archive without one is not a distribution this installer built — and
# every refusal names -SkipVerify as the deliberate override.
function Test-Binaries {
    $manifest = Join-Path $ScriptDir "SHA256SUMS"
    if (-not (Test-Path $manifest)) {
        Write-Error "SHA256SUMS is missing from this distribution; refusing to install an archive this installer did not build (pass -SkipVerify to install anyway)"
    }
    foreach ($bin in $Bins) {
        $want = $null
        foreach ($line in Get-Content $manifest) {
            $fields = $line -split "\s+"
            if ($fields.Count -ge 2 -and $fields[1] -eq $bin) {
                $want = $fields[0]
                break
            }
        }
        if (-not $want) {
            Write-Error "SHA256SUMS has no entry for $bin; refusing to install an unverifiable binary (pass -SkipVerify to install anyway)"
        }
        $got = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $ScriptDir $bin)).Hash.ToLower()
        if ($got -ne $want.ToLower()) {
            Write-Error "checksum mismatch for $bin: the archive's SHA256SUMS says $want, the file hashes to $got (pass -SkipVerify to install anyway)"
        }
    }
    Write-Host "verified $($Bins -join ' and ') against SHA256SUMS"
}

if ($Uninstall) {
    # A -ClientOnly uninstall removes the client and nothing else. It must
    # not drive `wt daemon uninstall`: this install registered no
    # coordinator, and inside a container the verb would refuse over the
    # host's registry entries — entries a container has no business
    # deciding about.
    if ($ClientOnly) {
        $wt = Join-Path $BinDir "wt.exe"
        if ($DryRun) {
            Write-Host "would remove: $wt"
            Write-Host "no coordinator was registered by a -ClientOnly install; none is deregistered"
            Write-Host "dry run: nothing was changed"
            exit 0
        }
        Remove-Item -Force $wt -ErrorAction SilentlyContinue
        Write-Host "removed wt.exe from $BinDir (no coordinator was registered by a -ClientOnly install)"
        exit 0
    }
    if ($DryRun) {
        # Print every action, change nothing.
        $wt = Join-Path $BinDir "wt.exe"
        if (Test-Path $wt) {
            $cmd = "& $wt daemon uninstall"
            if ($RegPrefix) { $cmd += " --prefix $RegPrefix" }
            Write-Host "would run: $cmd"
        } else {
            Write-Host "wt.exe is not installed in $BinDir; nothing to uninstall"
        }
        Write-Host "would remove: $(Join-Path $BinDir 'wt.exe') $(Join-Path $BinDir 'wtd.exe')"
        Write-Host "the store at $(Get-StorePath) is never removed"
        Write-Host "dry run: nothing was changed"
        exit 0
    }
    # Drive the verb first: it stops the coordinator, deregisters the logon
    # task and removes the task XML, and it refuses (exit 3, nothing
    # changed) while the registry still holds entries. Its exit code
    # propagates.
    $wt = Join-Path $BinDir "wt.exe"
    if (Test-Path $wt) {
        $regArgs = @()
        if ($RegPrefix) { $regArgs += "--prefix", $RegPrefix }
        & $wt daemon uninstall @regArgs
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    } else {
        Write-Host "wt.exe is not installed in $BinDir; nothing to uninstall"
        Write-Host "store: $(Get-StorePath) - left alone (it is the only record of what is allocated on this machine)"
    }
    Remove-Item -Force (Join-Path $BinDir "wt.exe"), (Join-Path $BinDir "wtd.exe") -ErrorAction SilentlyContinue
    Write-Host "removed wt.exe and wtd.exe from $BinDir"
    exit 0
}

# Verification first, before anything is copied. The dry run runs the same
# checks — they only read — and then prints every action without taking
# it. -SkipVerify skips the check in both.
if ($SkipVerify) {
    Write-Host "skipped verification (-SkipVerify)"
} else {
    Test-Binaries
}
Write-VersionLine

if ($DryRun) {
    Write-Host "would install $($Bins -join ' and ') into $BinDir"
    if ($ClientOnly) {
        Write-Host "client only: no coordinator is installed and nothing is registered"
        Write-Host "the client reaches a coordinator through WT_ENDPOINT and WT_CLIENT_TOKEN"
        Write-Host "dry run: nothing was changed"
        exit 0
    }
    if ($Addr) {
        Write-Host "address: $Addr (as given)"
    } else {
        Write-Host "address: a free port, chosen at install time"
    }
    if ($ContainerToken) {
        Write-Host "container clients: admitted (a token is configured; it is never echoed)"
    } else {
        Write-Host "container clients: not admitted (no -ContainerToken)"
    }
    # The supervisor command exactly as the real run would issue it; the
    # container token is a secret and is never echoed.
    $cmd = "& $(Join-Path $BinDir 'wt.exe') daemon install --wtd $(Join-Path $BinDir 'wtd.exe')"
    if ($RegPrefix) { $cmd += " --prefix $RegPrefix" }
    if ($Addr) { $cmd += " --addr $Addr" }
    if ($ContainerToken) { $cmd += " --container-token <token>" }
    foreach ($h in $AllowHost) { $cmd += " --allow-host $h" }
    Write-Host "would run: $cmd"
    Write-Host "dry run: nothing was changed"
    exit 0
}

# Copy to a temporary name in the same directory, then rename into place.
# Unlike unix, Windows genuinely cannot rename over a running executable —
# the open file image makes Move-Item fail with a sharing violation — so
# the coordinator's task is ended first; the `wt daemon install` below
# re-registers and restarts it. A task that does not exist or is not
# running makes schtasks fail, which is expected and ignored. A
# client-only install replaces no coordinator image, so it ends no task.
if (-not $ClientOnly) {
    & schtasks /End /TN "com.mrgeoffrich.wtd" 2>$null | Out-Null
}
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
foreach ($bin in $Bins) {
    $tmp = Join-Path $BinDir ".$bin.tmp"
    Copy-Item (Join-Path $ScriptDir $bin) $tmp -Force
    Move-Item -Force $tmp (Join-Path $BinDir $bin)
}
Write-Host "installed $($Bins -join ' and ') into $BinDir"

# A client-only install stops here: there is no coordinator to register,
# and the endpoint the client talks to is given at run time by the two
# environment variables rather than pinned at install time.
if ($ClientOnly) {
    Write-Host "client only: no coordinator was installed and nothing was registered"
    Write-Host "point the client at one with WT_ENDPOINT=http://<host>:<port> and WT_CLIENT_TOKEN=<token>"
    Write-Host "verify with: wt daemon status"
    exit 0
}

# Register the coordinator with the Task Scheduler and start it: the
# installer drives `wt daemon install`, which owns the logon-task
# registration. An explicit prefix directs the registration at the same
# prefix, where it is inert data.
$wt = Join-Path $BinDir "wt.exe"
$wtd = Join-Path $BinDir "wtd.exe"
$regArgs = @()
if ($RegPrefix -ne "") {
    $regArgs += "--prefix", $RegPrefix
}
# --addr and --container-token are passed independently: the coordinator
# has a default address and admits containers only when a token is
# configured, so neither implies the other.
if ($Addr -ne "") {
    $regArgs += "--addr", $Addr
}
if ($ContainerToken -ne "") {
    $regArgs += "--container-token", $ContainerToken
}
foreach ($h in $AllowHost) {
    $regArgs += "--allow-host", $h
}
& $wt daemon install @regArgs --wtd $wtd
if ($ContainerToken -ne "" -and $RegPrefix -eq "") {
    Write-Host "container clients admitted; a container sets WT_ENDPOINT to the address above and WT_CLIENT_TOKEN to the token"
}
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

Write-Host "verify with: wt daemon status"
$pathVar = [Environment]::GetEnvironmentVariable("Path", "User")
if ($pathVar -notlike "*$BinDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$BinDir;$pathVar", "User")
    Write-Host "note: added $BinDir to your user PATH (new terminals pick it up)"
}
