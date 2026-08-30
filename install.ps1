<#
.SYNOPSIS
    Installs the stamp CLI on Windows.

.DESCRIPTION
    Downloads a published stamp release from GitHub, verifies its SHA-256
    against the checksums file that ships beside it, and installs stamp.exe
    into a per-user directory that is put on the user PATH.

    The checksum step is the point of this script: stamp already promises a
    verified download for `stamp self-update`, so the installer must not be the
    weaker way in.

.EXAMPLE
    irm https://raw.githubusercontent.com/p-arndt/stamp/main/install.ps1 | iex

.EXAMPLE
    & ([scriptblock]::Create((irm https://raw.githubusercontent.com/p-arndt/stamp/main/install.ps1))) -Version 0.2.0
#>

# The param block has to be the first statement in the file: a script piped into
# iex cannot take parameters at all, so the documented way to pass them is to
# turn the downloaded text into a scriptblock, and that only binds parameters
# when they are declared up here.
param(
    [string]$Version,
    [string]$BinDir,
    [switch]$NoAddToPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Windows PowerShell 5.1 negotiates SSLv3/TLS 1.0 by default on older builds,
# which github.com refuses outright. Add TLS 1.2 rather than assigning it, so a
# session that already allows TLS 1.3 keeps it.
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
    # .NET builds that predate the Tls12 enum value cannot be fixed from here;
    # the download below will fail with its own message if TLS 1.2 is missing.
}

$Owner    = 'p-arndt'
$Repo     = 'stamp'
$ExeName  = 'stamp.exe'
$Releases = "https://github.com/$Owner/$Repo/releases"

function Write-Status([string]$Message) {
    # Progress belongs on the host, never in the pipeline: someone capturing the
    # output of this script should get nothing but what they asked for.
    Write-Host $Message
}

function Get-EnvOrDefault([string]$Name, [string]$Default) {
    $value = [Environment]::GetEnvironmentVariable($Name)
    if ([string]::IsNullOrWhiteSpace($value)) { return $Default }
    return $value
}

# Resolve-Architecture maps the machine to the GOARCH used in the asset names.
#
# OSArchitecture is asked first because it describes the operating system rather
# than the process; PROCESSOR_ARCHITECTURE lies in a 32-bit shell on a 64-bit
# box, which is why PROCESSOR_ARCHITEW6432 is consulted before it.
function Resolve-Architecture {
    $detected = $null
    try {
        $detected = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    } catch {
        # RuntimeInformation only exists from .NET Framework 4.7.1 onwards, so
        # fall through to the environment variables on anything older.
    }
    if ([string]::IsNullOrWhiteSpace($detected)) {
        $detected = Get-EnvOrDefault 'PROCESSOR_ARCHITEW6432' ''
    }
    if ([string]::IsNullOrWhiteSpace($detected)) {
        $detected = Get-EnvOrDefault 'PROCESSOR_ARCHITECTURE' ''
    }

    $goarch = $null
    switch -Regex ($detected) {
        '^(X64|AMD64)$' { $goarch = 'amd64' }
        '^ARM64$'       { $goarch = 'arm64' }
    }
    if (-not $goarch) {
        throw "stamp has no Windows build for this architecture (detected '$detected'). The published archives are listed at $Releases."
    }
    return [pscustomobject]@{ Detected = $detected; GoArch = $goarch }
}

# Invoke-GitHubApi calls the REST API, carrying GITHUB_TOKEN when one is set.
# The token goes on API calls only: the release assets are served from a
# redirect chain to a storage host that rejects a foreign Authorization header.
function Invoke-GitHubApi([string]$Uri) {
    $headers = @{ 'Accept' = 'application/vnd.github+json'; 'User-Agent' = 'stamp-install' }
    $token = Get-EnvOrDefault 'GITHUB_TOKEN' ''
    if ($token) { $headers['Authorization'] = "Bearer $token" }
    return Invoke-RestMethod -Uri $Uri -Headers $headers -UseBasicParsing
}

function Resolve-Version([string]$Requested) {
    if ($Requested -and $Requested -ne 'latest') {
        return $Requested -replace '^v', ''
    }
    Write-Status 'Resolving the latest release...'
    try {
        $release = Invoke-GitHubApi "https://api.github.com/repos/$Owner/$Repo/releases/latest"
    } catch {
        throw "Could not ask GitHub for the latest release: $($_.Exception.Message). Set GITHUB_TOKEN if this IP is rate limited, or pass -Version <x.y.z>."
    }
    # releases/latest skips prereleases on GitHub's side, which is exactly the
    # behaviour wanted here: a beta is never installed by accident.
    $tag = $release.tag_name
    if ([string]::IsNullOrWhiteSpace($tag)) {
        throw "GitHub returned a release without a tag name. Pass -Version <x.y.z> to install a specific version."
    }
    return $tag -replace '^v', ''
}

# Get-ExpectedHash pulls the hash for one exact file name out of a plain
# sha256sum listing ("<64 hex>  <name>"), and refuses to guess if the name is
# absent, because a missing line means the release is not the one we think it is.
function Get-ExpectedHash([string]$ChecksumsPath, [string]$FileName) {
    foreach ($line in (Get-Content -LiteralPath $ChecksumsPath)) {
        if ($line -match '^([0-9a-fA-F]{64})\s+\*?(.+)$') {
            if ($Matches[2].Trim() -eq $FileName) { return $Matches[1] }
        }
    }
    throw "The checksums file has no entry for $FileName, so the download cannot be verified. Nothing was installed."
}

function Install-Stamp {
    $arch = Resolve-Architecture

    $requestedVersion = $Version
    if ([string]::IsNullOrWhiteSpace($requestedVersion)) {
        $requestedVersion = Get-EnvOrDefault 'STAMP_VERSION' 'latest'
    }
    $resolvedVersion = Resolve-Version $requestedVersion

    $targetDir = $BinDir
    if ([string]::IsNullOrWhiteSpace($targetDir)) {
        $targetDir = Get-EnvOrDefault 'STAMP_BIN_DIR' (Join-Path $env:LOCALAPPDATA 'Programs\stamp')
    }

    $archiveName   = "stamp_${resolvedVersion}_windows_$($arch.GoArch).zip"
    $checksumsName = "stamp_${resolvedVersion}_checksums.txt"
    $base          = "$Releases/download/v$resolvedVersion"

    Write-Status "Installing stamp $resolvedVersion (windows/$($arch.GoArch)) into $targetDir"

    $work = Join-Path $env:TEMP ("stamp-install-" + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $work -Force | Out-Null

    # Invoke-WebRequest renders a progress bar per chunk on Windows PowerShell
    # 5.1, which turns a few-megabyte download into a minutes-long one. Silence
    # it for the transfers and put the caller's preference back afterwards.
    $previousProgress = $ProgressPreference
    try {
        $ProgressPreference = 'SilentlyContinue'

        $archivePath   = Join-Path $work $archiveName
        $checksumsPath = Join-Path $work $checksumsName

        Write-Status "Downloading $archiveName"
        try {
            Invoke-WebRequest -Uri "$base/$archiveName" -OutFile $archivePath -UseBasicParsing
        } catch {
            throw "Could not download $base/$archiveName : $($_.Exception.Message). Check that version $resolvedVersion exists at $Releases."
        }

        Write-Status "Downloading $checksumsName"
        try {
            Invoke-WebRequest -Uri "$base/$checksumsName" -OutFile $checksumsPath -UseBasicParsing
        } catch {
            throw "Could not download the checksums file $base/$checksumsName : $($_.Exception.Message). Refusing to install an unverified binary."
        }

        $expected = Get-ExpectedHash $checksumsPath $archiveName
        $actual   = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash
        # Get-FileHash returns uppercase and sha256sum writes lowercase, so the
        # comparison has to ignore case; -ne on strings already does.
        if ($actual -ne $expected) {
            # Write-Host rather than Write-Error for the two lines: with
            # $ErrorActionPreference = 'Stop' a Write-Error is itself
            # terminating, and the second hash would never be printed.
            Write-Host "expected $($expected.ToLowerInvariant())"
            Write-Host "actual   $($actual.ToLowerInvariant())"
            throw "Checksum mismatch for $archiveName (expected $($expected.ToLowerInvariant()), got $($actual.ToLowerInvariant())). The download was corrupted or tampered with; nothing was installed."
        }
        Write-Status "Checksum verified: $($actual.ToLowerInvariant())"

        $extracted = Join-Path $work 'extracted'
        Expand-Archive -LiteralPath $archivePath -DestinationPath $extracted -Force
        $source = Join-Path $extracted $ExeName
        if (-not (Test-Path -LiteralPath $source)) {
            throw "The archive $archiveName does not contain $ExeName. Report this at $Releases."
        }

        if (-not (Test-Path -LiteralPath $targetDir)) {
            New-Item -ItemType Directory -Path $targetDir -Force | Out-Null
        }
        $destination = Join-Path $targetDir $ExeName
        try {
            Copy-Item -LiteralPath $source -Destination $destination -Force
        } catch [System.IO.IOException], [System.UnauthorizedAccessException] {
            # Windows keeps a running executable locked, so upgrading over a
            # stamp that is currently executing fails here instead of quietly
            # writing nothing. The raw .NET message says nothing actionable.
            throw "Could not replace $destination : the file is in use or not writable. Close any window that is running stamp (a release waiting at its confirmation prompt counts), then run this installer again."
        }
        Write-Status "Installed $destination"

        Add-ToUserPath $targetDir
        Show-InstalledVersion $destination
    } finally {
        $ProgressPreference = $previousProgress
        # The temp directory is removed on the failure paths too, so a failed
        # install does not leave an unverified archive lying around.
        Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# Add-ToUserPath puts the install directory on the user PATH, which is the
# Windows convention and the reason this installer does more than the shell one.
# The Machine PATH is never touched: it needs elevation and it would change the
# environment of every account on the box.
function Add-ToUserPath([string]$Directory) {
    if ($NoAddToPath) {
        Write-Status "Not touching PATH (-NoAddToPath). Add $Directory yourself to run stamp by name."
        return
    }

    $normalized = $Directory.TrimEnd('\')
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($null -eq $userPath) { $userPath = '' }

    $alreadyThere = $false
    foreach ($entry in $userPath.Split(';')) {
        if ($entry.Trim().TrimEnd('\') -eq $normalized) { $alreadyThere = $true }
    }

    if ($alreadyThere) {
        Write-Status "$Directory is already on your user PATH."
    } else {
        $updated = $normalized
        if ($userPath.Trim() -ne '') {
            $updated = $userPath.TrimEnd(';') + ';' + $normalized
        }
        [Environment]::SetEnvironmentVariable('Path', $updated, 'User')
        Write-Status "Added $Directory to your user PATH."
    }

    # The process copy of PATH is inherited at start-up, so setting the user
    # value does nothing for the shell that is running right now. Patch it here
    # so `stamp` works immediately in this window.
    $sessionEntries = $env:Path.Split(';')
    $inSession = $false
    foreach ($entry in $sessionEntries) {
        if ($entry.Trim().TrimEnd('\') -eq $normalized) { $inSession = $true }
    }
    if (-not $inSession) {
        $env:Path = $env:Path.TrimEnd(';') + ';' + $normalized
    }
    Write-Status 'This window is ready to use; other terminals already open need restarting before they see the new PATH.'
}

# Show-InstalledVersion runs what was just installed, so the last thing printed
# is the binary's own account of itself rather than the installer's claim.
function Show-InstalledVersion([string]$Path) {
    try {
        $output = & $Path version 2>&1
        Write-Status ''
        foreach ($line in $output) { Write-Status $line }
    } catch {
        Write-Status "stamp was installed to $Path, but running `"$Path version`" failed: $($_.Exception.Message)"
    }
}

Install-Stamp
