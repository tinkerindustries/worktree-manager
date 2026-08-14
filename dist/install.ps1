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
# custom port.
param(
    [string]$Prefix = "",
    [string]$Addr = "",
    [string]$ContainerToken = $env:WT_CONTAINER_TOKEN
)

$ErrorActionPreference = "Stop"

function usage {
    @"
usage: .\install.ps1 [-Prefix <dir>] [-Addr <addr>] [-ContainerToken <tok>]

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
"@
}

if ($Prefix -eq "") {
    $Prefix = Join-Path $env:LOCALAPPDATA "wt"
    $RegPrefix = ""
} else {
    $RegPrefix = $Prefix
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
# --addr and --container-token are passed independently: the coordinator
# has a default address and admits containers only when a token is
# configured, so neither implies the other.
if ($Addr -ne "") {
    $regArgs += "--addr", $Addr
}
if ($ContainerToken -ne "") {
    $regArgs += "--container-token", $ContainerToken
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
