# install.ps1 - install the latest release of Bits on Windows, with
# PowerShell 5.1 or later:
#
#     irm -MaximumRedirection 0 https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts/install.ps1 | iex
#
# The release is unpacked into %LOCALAPPDATA%\bits\versions\<version>, and its
# The installer makes it current and puts %LOCALAPPDATA%\bits\current\bin on
# the user PATH. BITS_VERSION installs another release, v0.0.15 or later.
#
# Keep this file ASCII: PowerShell 5.1 decodes a downloaded script with the
# system code page.

# A function, so a download cut short runs nothing, and a throw stops the
# install without closing the window `irm | iex` runs in.
param([string]$Root, [string]$Version = $env:BITS_VERSION, [string]$UpdateFrom)

function Install-Bits {
    param([string]$Root, [string]$Version, [string]$UpdateFrom)
    $ErrorActionPreference = 'Stop'
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    function Get-BitsResponse([string]$Url, [string]$Method = 'GET') {
        $uri = [uri]$Url
        $secure = $uri.Scheme -eq 'https'
        foreach ($hop in 0..10) {
            if ($secure -and $uri.Scheme -ne 'https') { throw 'Refusing an insecure redirect.' }
            $request = [Net.WebRequest]::Create($uri)
            $request.Method = $Method
            $request.AllowAutoRedirect = $false
            $request.Timeout = 300000
            $request.ReadWriteTimeout = 300000
            $response = $request.GetResponse()
            if ([int]$response.StatusCode -lt 300 -or [int]$response.StatusCode -ge 400) { return $response }
            try {
                $location = $response.Headers['Location']
                if (-not $location) { throw 'A redirect has no Location.' }
                $uri = [uri]::new($uri, $location)
            } finally { $response.Close() }
        }
        throw 'Too many redirects.'
    }
    function Save-BitsDownload([string]$Url, [string]$Destination) {
        $response = Get-BitsResponse $Url
        try {
            $file = [IO.File]::Create($Destination)
            try { $response.GetResponseStream().CopyTo($file) } finally { $file.Dispose() }
        } finally { $response.Close() }
    }

    $releases = if ($env:BITS_RELEASES_URL) { $env:BITS_RELEASES_URL } else { 'https://github.com/datadog-labs/bits-cli/releases' }
    $requestedVersion = $version
    if (-not $version) {
        # releases/latest redirects to the tag page of the latest release,
        # which is how bits update finds it.
        $response = Get-BitsResponse "$releases/latest" 'HEAD'
        try {
            $latest = $response.ResponseUri.AbsolutePath
        } finally {
            $response.Close()
        }
        if ($latest -notmatch '/releases/tag/(v[^/]+)$') {
            throw "Could not find the latest release at $releases/latest."
        }
        $version = $Matches[1]
    }
    $version = 'v' + ($version -replace '^v', '')
    if ($version -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
        throw "$version is not a release version, such as v0.1.0."
    }
    # Managed archives before this release are not supported.
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

    if (-not $root) { $root = Join-Path $env:LOCALAPPDATA 'bits' }
    if (-not [IO.Path]::IsPathRooted($root)) { throw 'The installation root must be absolute.' }
    $root = [IO.Path]::GetFullPath($root).TrimEnd('\')
    if ($UpdateFrom -and ($UpdateFrom -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' -or [version]$UpdateFrom.Substring(1) -ge [version]$version.Substring(1))) {
        throw 'An update must select a newer version.'
    }
    # FileShare.None is a process-owned lock; a killed installer cannot leave
    # a stale lock. Serialize activation, including manual installs.
    New-Item -ItemType Directory -Force (Join-Path $root 'versions') | Out-Null
    $lock = $null
    $deadline = [DateTime]::UtcNow.AddMinutes(10)
    while ($null -eq $lock) {
        try {
            $lock = [IO.File]::Open((Join-Path $root 'install.lock'), [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
        } catch [IO.IOException] {
            if ([DateTime]::UtcNow -ge $deadline) { throw }
            Start-Sleep -Milliseconds 100
        }
    }
    $staging = Join-Path $root ('.staging-' + [IO.Path]::GetRandomFileName())
    try {
        New-Item -ItemType Directory $staging | Out-Null
        $current = Join-Path $root 'current'
        $previous = ''
        $selection = Get-Item -LiteralPath $current -Force -ErrorAction SilentlyContinue
        if ($selection -and ($selection.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            $previous = Split-Path -Leaf ([string]$selection.Target)
        } elseif ($selection -and (-not $selection.PSIsContainer -or @(Get-ChildItem -LiteralPath $current -Force).Count -ne 0)) {
            throw "$current is not an empty directory or installation junction."
        }
        if ($UpdateFrom -and $previous -ne $UpdateFrom) {
            Write-Host 'The active Bits installation changed; run bits update again if needed.'
            return
        }
        $previousVersion = $null
        if (-not $requestedVersion -and $previous -match '^v[0-9]+\.[0-9]+\.[0-9]+$' -and
            [version]::TryParse($previous.Substring(1), [ref]$previousVersion) -and $previousVersion -gt [version]$version.Substring(1)) {
            Write-Host "Keeping the newer Bits $previous in $root."
            $version = $previous
        }
        $dir = Join-Path $root "versions\$version"
        if (Test-Path -LiteralPath $dir) {
            if (-not (Test-Path -LiteralPath (Join-Path $dir '.install-complete') -PathType Leaf)) {
                throw "$dir is incomplete; remove it and try again."
            }
            $binary = Join-Path $dir 'bin\bits.exe'
        } else {
            $archive = "bits_$($version.Substring(1))_windows_$arch.zip"
            Write-Host "Downloading Bits $version for windows/$arch"
            $sums = Join-Path $staging 'checksums.txt'
            $zip = Join-Path $staging $archive
            Save-BitsDownload "$releases/download/$version/bits_$($version.Substring(1))_checksums.txt" $sums
            Save-BitsDownload "$releases/download/$version/$archive" $zip
            $expected = foreach ($line in Get-Content -LiteralPath $sums) {
                $fields = -split $line
                if ($fields.Count -eq 2 -and $fields[1].TrimStart('*') -eq $archive) { $fields[0] }
            }
            if (-not $expected) { throw "The release has no checksum for $archive." }
            if ((Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash -ne $expected) {
                throw "$archive does not match its checksum."
            }
            $prefix = Join-Path $staging 'prefix'
            Add-Type -AssemblyName System.IO.Compression.FileSystem
            $entries = [IO.Compression.ZipFile]::OpenRead($zip)
            try {
                foreach ($entry in $entries.Entries) {
                    $destination = [IO.Path]::GetFullPath((Join-Path $prefix $entry.FullName))
                    if (-not $destination.StartsWith($prefix + '\', [StringComparison]::OrdinalIgnoreCase)) {
                        throw 'Unsafe archive path.'
                    }
                    $type = ($entry.ExternalAttributes -shr 16) -band 61440
                    if ($type -ne 0 -and $type -ne 32768 -and $type -ne 16384) { throw 'Unsupported archive entry.' }
                }
            } finally { $entries.Dispose() }
            [IO.Compression.ZipFile]::ExtractToDirectory($zip, $prefix)
            if (-not (Test-Path -LiteralPath (Join-Path $prefix 'bin\bits.exe') -PathType Leaf)) {
                throw 'The archive has no executable bin\bits.exe.'
            }
            $binary = Join-Path $prefix 'bin\bits.exe'
        }
        # Invoke the literal executable path. PowerShell 5.1 can interpret
        # brackets in command paths as wildcard syntax.
        $start = [Diagnostics.ProcessStartInfo]@{
            FileName = $binary
            Arguments = '--version'
            UseShellExecute = $false
            RedirectStandardOutput = $true
        }
        $process = [Diagnostics.Process]::Start($start)
        try {
            $actualVersion = $process.StandardOutput.ReadToEnd().TrimEnd([char[]]"`r`n")
            $process.WaitForExit()
            $exitCode = $process.ExitCode
        } finally { $process.Dispose() }
        if ($exitCode -ne 0 -or $actualVersion -cne "bits version $version") {
            throw "Expected Bits $version; $binary reported $actualVersion (exit $exitCode)."
        }
        if ($binary -ne (Join-Path $dir 'bin\bits.exe')) {
            New-Item -ItemType File (Join-Path $prefix '.install-complete') | Out-Null
            # Scanners can still hold the executable briefly after it exits.
            for ($attempt = 0; ; $attempt++) {
                try {
                    [IO.Directory]::Move($prefix, $dir)
                    break
                } catch [IO.IOException], [UnauthorizedAccessException] {
                    $code = $_.Exception.GetBaseException().HResult -band 0xffff
                    if ($attempt -eq 4 -or $code -notin 5, 32, 33) { throw }
                    Start-Sleep -Milliseconds 250
                }
            }
        }

        # Updating a reparse point in place avoids a missing-current window and
        # shell expansion of %, &, or parentheses in directory names.
        if (-not ('BitsInstallerV1' -as [type])) {
            Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Text;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
public static class BitsInstallerV1 {
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    static extern SafeFileHandle CreateFile(string path, uint access, uint share, IntPtr security, uint disposition, uint flags, IntPtr template);
    [DllImport("kernel32.dll", SetLastError = true)]
    static extern bool DeviceIoControl(SafeFileHandle handle, uint control, byte[] input, int size, IntPtr output, int capacity, out int returned, IntPtr overlapped);
    [DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    static extern IntPtr SendMessageTimeout(IntPtr window, uint message, IntPtr wparam, string lparam, uint flags, uint timeout, out IntPtr result);
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    static extern uint GetFinalPathNameByHandle(SafeFileHandle handle, StringBuilder path, uint capacity, uint flags);
    public static string ResolveDirectory(string path) {
        using (var handle = CreateFile(path, 0, 7, IntPtr.Zero, 3, 0x02000000, IntPtr.Zero)) {
            if (handle.IsInvalid) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
            var name = new StringBuilder(32768);
            uint size = GetFinalPathNameByHandle(handle, name, (uint)name.Capacity, 0);
            if (size == 0) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
            if (size >= name.Capacity) throw new IOException("Installation root is too long.");
            string result = name.ToString();
            if (result.StartsWith(@"\\?\UNC\")) return @"\\" + result.Substring(8);
            return result.StartsWith(@"\\?\") ? result.Substring(4) : result;
        }
    }
    public static void SetJunction(string link, string target) {
        Directory.CreateDirectory(link);
        byte[] substitute = Encoding.Unicode.GetBytes(@"\??\" + target);
        byte[] display = Encoding.Unicode.GetBytes(target);
        byte[] buffer = new byte[16 + substitute.Length + display.Length + 4];
        if (buffer.Length > 16384) throw new IOException("Junction target is too long.");
        using (var stream = new MemoryStream(buffer))
        using (var writer = new BinaryWriter(stream)) {
            writer.Write((uint)0xA0000003);
            writer.Write((ushort)(buffer.Length - 8)); writer.Write((ushort)0);
            writer.Write((ushort)0); writer.Write((ushort)substitute.Length);
            writer.Write((ushort)(substitute.Length + 2)); writer.Write((ushort)display.Length);
            writer.Write(substitute); writer.Write((ushort)0); writer.Write(display); writer.Write((ushort)0);
        }
        using (var handle = CreateFile(link, 0x40000000, 7, IntPtr.Zero, 3, 0x02200000, IntPtr.Zero)) {
            if (handle.IsInvalid) throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
            int returned;
            if (!DeviceIoControl(handle, 0x000900A4, buffer, buffer.Length, IntPtr.Zero, 0, out returned, IntPtr.Zero))
                throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
        }
    }
    public static void BroadcastPath() {
        IntPtr result;
        if (SendMessageTimeout(new IntPtr(0xffff), 0x001a, IntPtr.Zero, "Environment", 2, 5000, out result) == IntPtr.Zero)
            throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
    }
}
'@
        }
        # Match the executable-based root used by bits update, including when
        # LOCALAPPDATA reaches it through a junction or another spelling.
        $root = [BitsInstallerV1]::ResolveDirectory($root)
        $current = Join-Path $root 'current'
        $dir = Join-Path $root "versions\$version"
        [BitsInstallerV1]::SetJunction($current, $dir)
        Write-Host "Installed Bits $version in $root"

        if ($UpdateFrom) { return }

        # Preserve both raw entries and their registry expansion behavior.
        $bin = Join-Path $current 'bin'
        if ($bin.Contains(';')) {
            Write-Warning "The installation path contains a PATH separator. Run $bin\bits.exe directly."
            return
        }
        try {
            $environment = if ($env:BITS_TEST_USER_ENVIRONMENT) { $env:BITS_TEST_USER_ENVIRONMENT } else { 'Environment' }
            $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey($environment)
            try {
                $userPath = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
                $kind = [Microsoft.Win32.RegistryValueKind]::String
                if ($key.GetValueNames() -contains 'Path') { $kind = $key.GetValueKind('Path') }
                $storedBin = $key.GetValue('BITS_INSTALL_BIN', $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
                if ($null -ne $storedBin) { $env:BITS_INSTALL_BIN = [string]$storedBin }
                $expand = $kind -eq [Microsoft.Win32.RegistryValueKind]::ExpandString
                $contains = $false
                foreach ($entry in ($userPath -split ';')) {
                    $candidate = $entry
                    if ($expand) { $candidate = [Environment]::ExpandEnvironmentVariables($entry) }
                    $literalPercent = $expand -and $bin.Contains('%') -and $entry.TrimEnd('\') -ieq $bin
                    if ($literalPercent -or -not $candidate) { continue }
                    if ($candidate.TrimEnd('\') -ieq $bin) {
                        $contains = $true
                        break
                    }
                    # Resolve the root without following current. A PATH entry
                    # pointing directly at one release would stop tracking updates.
                    $candidateRoot = [regex]::Match($candidate, '(?i)^([A-Z]:[\\/].*|\\\\.+)[\\/]current[\\/]bin[\\/]*$')
                    if (-not $candidateRoot.Success) { continue }
                    try {
                        if ([BitsInstallerV1]::ResolveDirectory($candidateRoot.Groups[1].Value) -ieq $root) {
                            $contains = $true
                            break
                        }
                    } catch [System.Management.Automation.MethodInvocationException] {
                        # An unrelated PATH entry may be missing or inaccessible.
                        continue
                    }
                }
                $entryToAdd = $bin
                $environmentChanged = $false
                if (-not $contains -and $expand -and $bin.Contains('%')) {
                    # Windows expands once. A REG_SZ indirection keeps literal
                    # %NAME% components without changing existing expandable entries.
                    $key.SetValue('BITS_INSTALL_BIN', $bin, [Microsoft.Win32.RegistryValueKind]::String)
                    $env:BITS_INSTALL_BIN = $bin
                    $entryToAdd = '%BITS_INSTALL_BIN%'
                    $contains = ($userPath -split ';') -contains $entryToAdd
                    $environmentChanged = $true
                }
                if (-not $contains) {
                    $newPath = if ($userPath) { "$entryToAdd;$userPath" } else { $entryToAdd }
                    $key.SetValue('Path', $newPath, $kind)
                    Write-Host "Added $bin to your PATH. Restart open terminals and editors to use bits there."
                    $environmentChanged = $true
                }
                if ($environmentChanged) {
                    try { [BitsInstallerV1]::BroadcastPath() } catch { Write-Warning $_ }
                }
            } finally { $key.Dispose() }
            if (($env:Path -split ';') -notcontains $bin) { $env:Path = "$bin;$env:Path" }
            $first = Get-Command bits.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
            if (-not $first) {
                Write-Warning "Could not find bits on your PATH. Run $bin\bits.exe directly."
            } elseif ($first.Source -ine (Join-Path $bin 'bits.exe')) {
                Write-Warning "$($first.Source) comes first on your PATH, so run $bin\bits.exe to use this one."
            } else {
                Write-Host 'Run bits in this PowerShell session.'
                Write-Host "If a new terminal finds another copy first, run $bin\bits.exe."
            }
        } catch { Write-Warning "Could not set up PATH. Run $bin\bits.exe directly. $_" }
    } finally {
        try { if (Test-Path -LiteralPath $staging) { Remove-Item -LiteralPath $staging -Recurse -Force } } finally { $lock.Dispose() }
    }
}

& { Install-Bits -Root $Root -Version $Version -UpdateFrom $UpdateFrom }
