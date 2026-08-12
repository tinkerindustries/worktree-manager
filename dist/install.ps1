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
# The opt-in loopback TCP surface is configured with -Tcp <addr> and
# -TcpToken <token> (both together, loopback address, 16+ characters) —
# for hosts where a socket cannot be shared into a container.
param(
    [string]$Prefix = "",
    [string]$Tcp = "",
    [string]$TcpToken = $env:WT_TCP_TOKEN
)

$ErrorActionPreference = "Stop"

function usage {
    @"
usage: .\install.ps1 [-Prefix <dir>] [-Tcp <addr> -TcpToken <token>]

  -Prefix <dir>     install the binaries into <dir>\bin and write the task
                    XML under <dir> (self-contained; nothing is
                    registered). Default: %%LOCALAPPDATA%%\wt\bin with a
                    real logon-task registration.
  -Tcp <addr>       also start the coordinator's opt-in loopback TCP
                    listener at this address (requires -TcpToken).
  -TcpToken <tok>   the token every TCP connection must present (at least
                    16 characters; WT_TCP_TOKEN also works).
"@
}

if ($Prefix -eq "") {
    $Prefix = Join-Path $env:LOCALAPPDATA "wt"
    $RegPrefix = ""
} else {
    $RegPrefix = $Prefix
}

if ($Tcp -ne "" -and $TcpToken -eq "") {
    Write-Error "--Tcp requires --TcpToken: peer credentials do not exist on a TCP connection, so the listener is unauthenticated without one"
}

# The archive's own directory: the two binaries must be right next to this
# script, which is how the distribution is assembled.
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
foreach ($bin in @("wt.exe", "wtd.exe")) {
    if (-not (Test-Path (Join-Path $ScriptDir $bin))) {
        Write-Error "$bin is missing from this distribution (install from the archive, not from a bare copy)"
    }
}

$BinDir = Join-Path $Prefix "bin"
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
Copy-Item (Join-Path $ScriptDir "wt.exe") $BinDir -Force
Copy-Item (Join-Path $ScriptDir "wtd.exe") $BinDir -Force
Write-Host "installed wt.exe and wtd.exe into $BinDir"

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
if ($Tcp -ne "") {
    & $wt daemon install @regArgs --wtd $wtd --tcp $Tcp --tcp-token $TcpToken
    if ($RegPrefix -eq "") {
        Write-Host "loopback TCP enabled on $Tcp; a container client dials tcp://<host>:<port> with WT_CLIENT_TOKEN set"
    }
} else {
    & $wt daemon install @regArgs --wtd $wtd
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
