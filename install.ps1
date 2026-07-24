<#
.SYNOPSIS
    Kagi CLI installer for Windows (amd64/arm64).

.DESCRIPTION
    Intended to be hosted at https://get.kagi.pw/install.ps1 and run as:
        iwr https://get.kagi.pw/install.ps1 -useb | iex

    Downloads the latest 'kagi' release zip from github.com/senseylabs/kagi-cli,
    verifies it against the published checksums file, extracts kagi.exe, and
    installs it into a user-writable bin directory added to PATH.
#>

[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$Repo    = "senseylabs/kagi-cli"
$Project = "kagi-cli"
$Binary  = "kagi.exe"

function Write-Info {
    param([string]$Message)
    Write-Host "==> $Message"
}

function Fail {
    param([string]$Message)
    Write-Error "error: $Message"
    exit 1
}

# --- detect architecture -------------------------------------------------

function Get-KagiArch {
    $archRaw = $env:PROCESSOR_ARCHITECTURE
    if ([Environment]::Is64BitOperatingSystem -eq $false) {
        Fail "unsupported CPU architecture: 32-bit Windows is not supported (kagi ships amd64 and arm64 binaries only)"
    }
    switch -Regex ($archRaw) {
        "^(AMD64|x86_64)$" { return "amd64" }
        "^(ARM64|aarch64)$" { return "arm64" }
        default {
            # Fall back to .NET's runtime architecture if PROCESSOR_ARCHITECTURE is unhelpful.
            $procArch = [System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture
            switch ($procArch) {
                "X64"   { return "amd64" }
                "Arm64" { return "arm64" }
                default { Fail "unsupported CPU architecture: $archRaw / $procArch (kagi ships amd64 and arm64 binaries only)" }
            }
        }
    }
}

try {
    $Arch = Get-KagiArch
} catch {
    Fail "failed to detect CPU architecture: $($_.Exception.Message)"
}

Write-Info "detected platform: windows/$Arch"

# --- resolve latest release tag -------------------------------------------

$ApiUrl = "https://api.github.com/repos/$Repo/releases/latest"
Write-Info "resolving latest release from $ApiUrl"

try {
    $Headers = @{ "User-Agent" = "kagi-cli-installer" }
    $Release = Invoke-RestMethod -Uri $ApiUrl -Headers $Headers -Method Get
} catch {
    Fail "failed to reach GitHub API at $ApiUrl (network issue or repo not public yet): $($_.Exception.Message)"
}

$Tag = $Release.tag_name
if ([string]::IsNullOrWhiteSpace($Tag)) {
    Fail "could not parse a release tag from the GitHub API response; the repo may have no releases yet"
}

# goreleaser's {{.Version}} strips a leading 'v' from the tag.
$Version = $Tag.TrimStart("v")

Write-Info "latest release: $Tag (version $Version)"

# --- download archive + checksums -----------------------------------------

$Archive        = "${Project}_${Version}_windows_${Arch}.zip"
$ChecksumsFile  = "${Project}_${Version}_checksums.txt"
$BaseUrl        = "https://github.com/$Repo/releases/download/$Tag"
$ArchiveUrl     = "$BaseUrl/$Archive"
$ChecksumsUrl   = "$BaseUrl/$ChecksumsFile"

$WorkDir = Join-Path ([System.IO.Path]::GetTempPath()) "kagi-install-$([guid]::NewGuid())"
New-Item -ItemType Directory -Path $WorkDir -Force | Out-Null

try {
    $ArchivePath    = Join-Path $WorkDir $Archive
    $ChecksumsPath  = Join-Path $WorkDir $ChecksumsFile

    Write-Info "downloading $ArchiveUrl"
    try {
        Invoke-WebRequest -Uri $ArchiveUrl -OutFile $ArchivePath -UseBasicParsing
    } catch {
        Fail "failed to download $ArchiveUrl (no release asset for windows/$Arch, or network error): $($_.Exception.Message)"
    }

    Write-Info "downloading $ChecksumsUrl"
    try {
        Invoke-WebRequest -Uri $ChecksumsUrl -OutFile $ChecksumsPath -UseBasicParsing
    } catch {
        Fail "failed to download checksums file $ChecksumsUrl : $($_.Exception.Message)"
    }

    # --- verify checksum ---------------------------------------------------

    Write-Info "verifying checksum"

    $ChecksumLine = Select-String -Path $ChecksumsPath -Pattern ([Regex]::Escape($Archive)) | Select-Object -First 1
    if (-not $ChecksumLine) {
        Fail "no checksum entry found for $Archive in $ChecksumsFile"
    }
    $ExpectedSum = ($ChecksumLine.Line -split "\s+")[0].ToLowerInvariant()

    $ActualSum = (Get-FileHash -Path $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()

    if ($ExpectedSum -ne $ActualSum) {
        Fail "checksum mismatch for $Archive`: expected $ExpectedSum, got $ActualSum (download may be corrupted or tampered with)"
    }

    Write-Info "checksum OK"

    # --- extract -------------------------------------------------------

    Write-Info "extracting $Binary"
    try {
        Expand-Archive -Path $ArchivePath -DestinationPath $WorkDir -Force
    } catch {
        Fail "failed to extract $Archive : $($_.Exception.Message)"
    }

    $ExtractedBinary = Join-Path $WorkDir $Binary
    if (-not (Test-Path $ExtractedBinary)) {
        Fail "extracted archive did not contain a '$Binary' binary"
    }

    # --- install ---------------------------------------------------------

    $InstallDir = Join-Path $env:LOCALAPPDATA "Kagi\bin"
    try {
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    } catch {
        Fail "failed to create install directory $InstallDir : $($_.Exception.Message)"
    }

    $TargetPath = Join-Path $InstallDir $Binary
    Write-Info "installing to $TargetPath"
    try {
        Copy-Item -Path $ExtractedBinary -Destination $TargetPath -Force
    } catch {
        Fail "failed to copy binary into $InstallDir : $($_.Exception.Message)"
    }

    # --- add to user PATH -------------------------------------------------

    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($null -eq $UserPath) { $UserPath = "" }
    $PathEntries = $UserPath -split ";" | Where-Object { $_ -ne "" }

    if ($PathEntries -notcontains $InstallDir) {
        try {
            $NewPath = if ($UserPath.Trim() -eq "") { $InstallDir } else { "$UserPath;$InstallDir" }
            [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
            $env:Path = "$env:Path;$InstallDir"
            Write-Info "added $InstallDir to your User PATH (restart your shell to pick it up)"
        } catch {
            Write-Warning "could not update User PATH automatically: $($_.Exception.Message)"
            Write-Warning "add this directory to PATH manually: $InstallDir"
        }
    } else {
        $env:Path = "$env:Path;$InstallDir"
    }

    # --- verify ------------------------------------------------------------

    try {
        $InstalledVersion = & $TargetPath --version 2>&1
    } catch {
        Fail "installed binary at $TargetPath failed to run '--version': $($_.Exception.Message)"
    }

    Write-Info "installed: $InstalledVersion"
    Write-Info "kagi $Version installed successfully to $TargetPath"
} finally {
    Remove-Item -Path $WorkDir -Recurse -Force -ErrorAction SilentlyContinue
}
