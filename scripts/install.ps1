# install.ps1 - install Bits on Windows from a GitHub release, with
# PowerShell 5.1 or later:
#
#     irm https://github.com/datadog-labs/bits-cli/releases/latest/download/install.ps1 | iex
#
# The release is unpacked into %LOCALAPPDATA%\bits\versions\<version>, and its
# `bits install` makes it current and puts %LOCALAPPDATA%\bits\current\bin on
# the user PATH. BITS_VERSION installs another release, v0.0.15 or later.
#
# Keep this file ASCII: PowerShell 5.1 decodes a downloaded script with the
# system code page.

# A function, so a download cut short runs nothing, and a throw stops the
# install without closing the window `irm | iex` runs in.
function Install-Bits {
    $ErrorActionPreference = 'Stop'
    # The progress bar slows Invoke-WebRequest down many times over in 5.1.
    $ProgressPreference = 'SilentlyContinue'
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    # The release this script installs; package.sh sets it in release assets.
    $release = ''

    $releases = if ($env:BITS_RELEASES_URL) { $env:BITS_RELEASES_URL } else { 'https://github.com/datadog-labs/bits-cli/releases' }
    $version = if ($env:BITS_VERSION) { $env:BITS_VERSION } else { $release }
    if (-not $version) {
        throw 'Set BITS_VERSION to the release to install, such as v0.1.0.'
    }
    $version = 'v' + ($version -replace '^v', '')
    if ($version -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
        throw "$version is not a release version, such as v0.1.0."
    }
    # Older releases have no `bits install`.
    if ([version]$version.Substring(1) -lt [version]'0.0.15') {
        throw "Bits $version predates this installer; install v0.0.15 or later."
    }

    # The machine's architecture, not this process's: x64 PowerShell also
    # runs on ARM64.
    $machine = (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment').PROCESSOR_ARCHITECTURE
    $arch = switch ($machine) {
        'AMD64' { 'amd64' }
        'ARM64' { 'arm64' }
        default { throw "$machine processors are not supported." }
    }

    $root = Join-Path $env:LOCALAPPDATA 'bits'
    $archive = "bits_$($version.Substring(1))_windows_$arch.zip"
    $dir = Join-Path $root "versions\$version"
    $staging = Join-Path $root ('.staging-' + [IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Force (Join-Path $root 'versions') | Out-Null
    New-Item -ItemType Directory $staging | Out-Null
    try {
        Write-Host "Downloading Bits $version for windows/$arch"
        $sums = Join-Path $staging 'checksums.txt'
        $zip = Join-Path $staging $archive
        Invoke-WebRequest -UseBasicParsing "$releases/download/$version/bits_$($version.Substring(1))_checksums.txt" -OutFile $sums
        Invoke-WebRequest -UseBasicParsing "$releases/download/$version/$archive" -OutFile $zip
        $expected = foreach ($line in Get-Content $sums) {
            $fields = -split $line
            if ($fields.Count -eq 2 -and $fields[1].TrimStart('*') -eq $archive) { $fields[0] }
        }
        if (-not $expected) {
            throw "The release has no checksum for $archive."
        }
        if ((Get-FileHash -Algorithm SHA256 $zip).Hash -ne $expected) {
            throw "$archive does not match its checksum."
        }

        $prefix = Join-Path $staging 'prefix'
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        [IO.Compression.ZipFile]::ExtractToDirectory($zip, $prefix)
        New-Item -ItemType File (Join-Path $prefix '.install-complete') | Out-Null
        # A complete directory is this version from an earlier or concurrent
        # install, which may be running: never delete it. An incomplete one is
        # a removal that stopped halfway, which moves aside into staging.
        if (-not (Test-Path (Join-Path $dir '.install-complete'))) {
            if (Test-Path $dir) {
                Move-Item $dir (Join-Path $staging 'incomplete')
            }
            Move-Item $prefix $dir
            # When a concurrent install creates $dir first, Move-Item moves into it.
            $nested = Join-Path $dir 'prefix'
            if (Test-Path $nested) {
                Remove-Item -Recurse -Force $nested
            }
        }
    } finally {
        Remove-Item -Recurse -Force $staging
    }

    # bits install puts current\bin on the user PATH for new windows; this one
    # needs it too.
    $bin = Join-Path $root 'current\bin'
    if (($env:Path -split ';') -notcontains $bin) {
        $env:Path = "$bin;$env:Path"
    }
    # Under Stop, ISE and remoting turn the first line bits writes to stderr
    # into an error; the exit code says whether it worked.
    $ErrorActionPreference = 'Continue'
    & (Join-Path $dir 'bin\bits.exe') install
    if ($LASTEXITCODE -ne 0) {
        throw "bits install failed with exit code $LASTEXITCODE."
    }
}

Install-Bits
