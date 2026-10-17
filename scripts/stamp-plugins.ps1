# Stamp the embedded plugin snapshot with the commit it came from.
#
# The binary ships a copy of the curated plugins so a fresh install works
# offline. That copy needs to carry the identity of the commit it was taken
# from, otherwise the tool cannot answer "is my plugin set current" and every
# install re-downloads on first sync.
#
# Run this after syncing plugins into internal/plugins/ and before tagging.
#
#   pwsh -File scripts/stamp-plugins.ps1

[CmdletBinding()]
param(
    # Repository holding the plugins branch.
    [string]$Repo = 'morning-start/keep-mirrors',
    # Branch to take the SHA from.
    [string]$Branch = 'plugins',
    # Skip the check that the local files match the branch.
    [switch]$Force
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$pluginDir = Join-Path $root 'internal/plugins'
$provenance = Join-Path $pluginDir 'PROVENANCE'

if (-not (Test-Path $pluginDir)) {
    throw "plugin directory not found: $pluginDir"
}

# Resolve the branch without the REST API: git ls-remote speaks the git
# protocol and needs no token, while the API allows only 60 anonymous calls an
# hour.
Write-Host "resolving $Repo@$Branch ..."
$out = & git ls-remote --heads "https://github.com/$Repo.git" $Branch 2>&1
if ($LASTEXITCODE -ne 0) {
    throw "git ls-remote failed: $out"
}
$line = ($out | Where-Object { $_ -match "refs/heads/$Branch$" } | Select-Object -First 1)
if (-not $line) {
    throw "branch $Branch not found in $Repo"
}
$sha = ($line -split '\s+')[0]
if ($sha -notmatch '^[0-9a-f]{40}$') {
    throw "unexpected SHA from ls-remote: $sha"
}
Write-Host "branch head: $sha"

# Refuse to stamp a SHA that does not describe the local files. Stamping a
# mismatched pair would produce a binary that either claims to be newer than it
# is (hiding a pending update) or older (forcing a needless one).
if (-not $Force) {
    $plugins = Get-ChildItem -Path $pluginDir -Filter *.lua | Sort-Object Name
    if ($plugins.Count -eq 0) {
        throw "no .lua files in $pluginDir"
    }
    $drift = @()
    foreach ($f in $plugins) {
        $local = (& git hash-object $f.FullName).Trim()
        $remote = (& git -C $root rev-parse "${sha}:internal/plugins/$($f.Name)" 2>$null)
        if ($LASTEXITCODE -ne 0 -or -not $remote) {
            $drift += "$($f.Name): not present at $sha"
            continue
        }
        if ($local -ne $remote.Trim()) {
            $drift += "$($f.Name): local content differs from $sha"
        }
    }
    if ($drift.Count -gt 0) {
        Write-Host ''
        Write-Host 'The snapshot does not match the branch:' -ForegroundColor Red
        $drift | ForEach-Object { Write-Host "  $_" -ForegroundColor Red }
        Write-Host ''
        Write-Host 'Sync first, then stamp:' -ForegroundColor Yellow
        Write-Host "  git checkout $Branch -- internal/plugins/" -ForegroundColor Yellow
        Write-Host ''
        Write-Host 'Use -Force only if you intend to record a SHA the files do not match.' -ForegroundColor Yellow
        exit 1
    }
    Write-Host "all $($plugins.Count) plugin(s) match $($sha.Substring(0,7))"
}

$body = @(
    "sha=$sha",
    "source=https://github.com/$Repo",
    "branch=$Branch"
) -join "`n"
Write-Host "writing $provenance"
# Write without a BOM. Set-Content -Encoding utf8 on PowerShell 5 always emits
# one, and a BOM turns the first key into "\ufeffsha" for anything that parses
# the file strictly. The Go side strips it defensively, but not writing it is
# the actual fix.
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($provenance, $body + "`n", $utf8NoBom)

Write-Host ''
Write-Host "stamped $sha" -ForegroundColor Green
Write-Host 'verify with: go test ./internal/plugins/'
