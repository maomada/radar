param(
    [ValidateSet("", "success", "mismatch", "missing", "upstream", "unknown")]
    [string]$Scenario = ""
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$FixtureRoot = Join-Path ([System.IO.Path]::GetTempPath()) "radar-installer-fixtures-$([guid]::NewGuid())"

function Fail([string]$Message) {
    throw $Message
}

if ($Scenario) {
    $InstallDir = Join-Path $env:LOCALAPPDATA "radar"
    $Filename = "radar_v0.0.0_windows_amd64.zip"
    $Archive = Join-Path $env:RADAR_FIXTURE_ROOT $Filename
    $Checksum = Join-Path $env:RADAR_FIXTURE_ROOT "checksums.txt"
    $Latest = "https://github.com/maomada/radar/releases/latest"
    $Tag = if ($Scenario -eq "upstream") {
        "https://github.com/skyhook-io/radar/releases/tag/v0.0.0"
    } elseif ($Scenario -eq "unknown") {
        "https://example.com/releases/tag/v0.0.0"
    } else {
        "https://github.com/maomada/radar/releases/tag/v0.0.0"
    }

    function Invoke-WebRequest {
        param(
            [string]$Uri,
            [string]$OutFile,
            [switch]$UseBasicParsing,
            [int]$MaximumRedirection
        )
        if ($Uri -eq $Latest) {
            if ($PSVersionTable.PSVersion.Major -le 5) {
                return [pscustomobject]@{
                    BaseResponse = [pscustomobject]@{ ResponseUri = [uri]$Tag }
                }
            }
            return [pscustomobject]@{
                BaseResponse = [pscustomobject]@{
                    RequestMessage = [pscustomobject]@{ RequestUri = [uri]$Tag }
                }
            }
        }
        $Download = "https://github.com/maomada/radar/releases/download/v0.0.0/$Filename"
        $Checksums = "https://github.com/maomada/radar/releases/download/v0.0.0/checksums.txt"
        if ($Uri -eq $Download) {
            Copy-Item -LiteralPath $Archive -Destination $OutFile
            return
        }
        if ($Uri -eq $Checksums) {
            Copy-Item -LiteralPath $Checksum -Destination $OutFile
            return
        }
        throw "unexpected installer URL: $Uri"
    }

    . "$Root/install.ps1"
    $InstallerExitCode = if ($null -eq $LASTEXITCODE) { 0 } else { $LASTEXITCODE }
    if ($Scenario -eq "success") {
        if ($InstallerExitCode -ne 0) { Fail "Windows success fixture returned exit code $InstallerExitCode" }
        $Binary = Join-Path $InstallDir "kubectl-radar.exe"
        $Alias = Join-Path $InstallDir "radar.exe"
        if (-not (Test-Path -LiteralPath $Binary)) { Fail "missing Windows fixture binary" }
        if (-not (Test-Path -LiteralPath $Alias)) { Fail "missing Windows fixture alias" }
        if ((Get-Content -Raw -LiteralPath $Binary) -ne "radar fixture payload") {
            Fail "unexpected Windows fixture payload"
        }
    } else {
        if ($InstallerExitCode -eq 0) { Fail "Windows $Scenario fixture unexpectedly succeeded" }
        if (Test-Path -LiteralPath $InstallDir) {
            Fail "rejected Windows install created its install directory"
        }
    }
    exit $InstallerExitCode
}

New-Item -ItemType Directory -Path $FixtureRoot -Force | Out-Null
$Payload = Join-Path $FixtureRoot "kubectl-radar.exe"
$Archive = Join-Path $FixtureRoot "radar_v0.0.0_windows_amd64.zip"
$Checksum = Join-Path $FixtureRoot "checksums.txt"
Set-Content -LiteralPath $Payload -Value "radar fixture payload" -NoNewline
Compress-Archive -LiteralPath $Payload -DestinationPath $Archive
$Hash = (Get-FileHash -LiteralPath $Archive -Algorithm SHA256).Hash.ToLowerInvariant()
Set-Content -LiteralPath $Checksum -Value "$Hash  radar_v0.0.0_windows_amd64.zip"

$OriginalUserPath = [Environment]::GetEnvironmentVariable("Path", "User")
$OriginalLocalAppData = $env:LOCALAPPDATA
$OriginalTemp = $env:TEMP
$PowerShellExe = if ($PSVersionTable.PSEdition -eq "Core") {
    Join-Path $PSHOME "pwsh.exe"
} else {
    Join-Path $PSHOME "powershell.exe"
}
try {
    foreach ($Case in @("success", "mismatch", "missing", "upstream", "unknown")) {
        $CaseRoot = Join-Path $FixtureRoot $Case
        New-Item -ItemType Directory -Path $CaseRoot -Force | Out-Null
        $env:LOCALAPPDATA = Join-Path $CaseRoot "localappdata"
        $env:TEMP = Join-Path $CaseRoot "temp"
        $env:RADAR_FIXTURE_ROOT = $FixtureRoot
        New-Item -ItemType Directory -Path $env:LOCALAPPDATA, $env:TEMP -Force | Out-Null
        if ($Case -eq "mismatch") {
            Set-Content -LiteralPath $Checksum -Value ("0" * 64 + "  radar_v0.0.0_windows_amd64.zip")
        } elseif ($Case -eq "missing") {
            Set-Content -LiteralPath $Checksum -Value "$Hash  other.zip"
        } else {
            Set-Content -LiteralPath $Checksum -Value "$Hash  radar_v0.0.0_windows_amd64.zip"
        }
        $OutputLog = Join-Path $CaseRoot "output.log"
        $ErrorLog = Join-Path $CaseRoot "error.log"
        $Child = Start-Process -FilePath $PowerShellExe `
            -ArgumentList @("-NoProfile", "-File", $PSCommandPath, "-Scenario", $Case) `
            -RedirectStandardOutput $OutputLog -RedirectStandardError $ErrorLog -PassThru -Wait
        if ($Case -eq "success" -and $Child.ExitCode -ne 0) {
            Get-Content -LiteralPath $OutputLog, $ErrorLog
            Fail "Windows success fixture failed"
        }
        if ($Case -ne "success" -and $Child.ExitCode -eq 0) {
            Get-Content -LiteralPath $OutputLog, $ErrorLog
            Fail "Windows $Case fixture unexpectedly succeeded"
        }
        if ($Case -ne "success") {
            $FailureOutput = (Get-Content -LiteralPath $OutputLog -Raw) + (Get-Content -LiteralPath $ErrorLog -Raw)
            $ExpectedFailure = switch ($Case) {
                "mismatch" { "Checksum verification failed for" }
                "missing" { "Failed to find checksum for" }
                default { "unexpected release URL" }
            }
            if ($FailureOutput -notlike "*$ExpectedFailure*") {
                Get-Content -LiteralPath $OutputLog, $ErrorLog
                Fail "Windows $Case fixture did not fail for the expected reason"
            }
        }
        if ($Case -ne "success" -and (Test-Path -LiteralPath (Join-Path $env:LOCALAPPDATA "radar"))) {
            Fail "Windows $Case fixture left an install directory"
        }
    }
} finally {
    [Environment]::SetEnvironmentVariable("Path", $OriginalUserPath, "User")
    $env:LOCALAPPDATA = $OriginalLocalAppData
    $env:TEMP = $OriginalTemp
    Remove-Item -LiteralPath $FixtureRoot -Recurse -Force -ErrorAction SilentlyContinue
}

if ([Environment]::GetEnvironmentVariable("Path", "User") -ne $OriginalUserPath) {
    Fail "Windows fixture did not restore the user PATH"
}
Write-Host "Windows installer fixtures passed"
