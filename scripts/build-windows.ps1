# Builds the exe and the setup program into dist\ (#166).
#   powershell -ExecutionPolicy Bypass -File scripts\build-windows.ps1 [-Version 1.2.4] [-Stage all|exe|installer]
# -Version is without the v; the exe reports v<Version>. Without it the
# version comes from `git describe`. The release workflow runs the stages
# apart, to code sign the exe before the setup packs it and the setup after.
# The setup needs Inno Setup 6 (winget install JRSoftware.InnoSetup).
param([string]$Version, [ValidateSet('all', 'exe', 'installer')][string]$Stage = 'all')

$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)

function Invoke-Checked {
    param([string]$Exe, [string[]]$Arguments)
    & $Exe @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Exe failed with exit code $LASTEXITCODE" }
}

if (-not $Version) {
    $described = (git describe --tags --always --match 'v*' 2>$null)
    if ($described -match '^v?(\d+\.\d+\.\d+.*)$') { $Version = $Matches[1] } else { $Version = '0.0.0-dev' }
}
$Version = $Version.TrimStart('v')
$numeric = ([regex]::Match($Version, '^\d+\.\d+\.\d+')).Value
if (-not $numeric) { throw "Version '$Version' is not x.y.z" }
Write-Host "Version v$Version"

New-Item -ItemType Directory -Force dist | Out-Null

if ($Stage -ne 'installer') {
    # The same build as ci.yml (so a tag build reuses its cache). Pure Go
    # since the Gio UI (#165); the icon and the manifest come from
    # rsrc_windows_amd64.syso.
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    Invoke-Checked go @('build',
        '-ldflags', "-s -w -H=windowsgui -X main.Version=v$Version",
        '-o', 'dist\smartthings-pc-control.exe', '.')
}

if ($Stage -eq 'exe') {
    Get-ChildItem dist | Format-Table Name, Length
    return
}

$iscc = $null
$found = Get-Command iscc.exe -ErrorAction SilentlyContinue
if ($found) { $iscc = $found.Source }
foreach ($candidate in @("$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe", "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe", "$env:ProgramFiles\Inno Setup 6\ISCC.exe")) {
    if (-not $iscc -and (Test-Path $candidate)) { $iscc = $candidate }
}
if (-not $iscc) { throw 'Inno Setup 6 not found (winget install JRSoftware.InnoSetup)' }

Invoke-Checked $iscc @('/Qp',
    "/DAppVersion=$Version",
    "/DAppVersionNumeric=$numeric.0",
    'installer\smartthings-pc-control.iss')

Get-ChildItem dist | Format-Table Name, Length
