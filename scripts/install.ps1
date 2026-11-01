# Installs the latest xf release on Windows:
#
#   irm https://raw.githubusercontent.com/ashish10alex/xf/main/scripts/install.ps1 | iex
#
# $env:XF_VERSION picks a release (e.g. v0.0.1) instead of the latest, and
# $env:XF_INSTALL_DIR the folder xf.exe goes in (default
# %LocalAppData%\Programs\xf), which is added to the user PATH. The zip is
# checked against the release's checksums.txt before anything is installed.
# To build from source instead, see scripts/build-install.ps1.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # Invoke-WebRequest is slow with it
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo = 'ashish10alex/xf'
$dir = if ($env:XF_INSTALL_DIR) { $env:XF_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\xf' }

# The OS's architecture, not this PowerShell's: x64 PowerShell on ARM64
# Windows would otherwise pick the emulated build.
$arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    'X64' { 'amd64' }
    'Arm64' { 'arm64' }
    default { throw "xf: unsupported architecture $_" }
}
$base = if ($env:XF_VERSION) { "https://github.com/$repo/releases/download/$env:XF_VERSION" } else { "https://github.com/$repo/releases/latest/download" }
$zip = "xf_windows_$arch.zip"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("xf-install-" + [guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null
try {
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$zip" -OutFile (Join-Path $tmp $zip)
    Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt')
    $want = Get-Content (Join-Path $tmp 'checksums.txt') | ForEach-Object { $f = $_ -split '\s+'; if ($f[1] -eq $zip) { $f[0] } }
    $got = (Get-FileHash (Join-Path $tmp $zip) -Algorithm SHA256).Hash.ToLower()
    if (-not $want -or $want -ne $got) { throw "xf: $zip doesn't match checksums.txt; nothing installed" }
    Expand-Archive (Join-Path $tmp $zip) -DestinationPath (Join-Path $tmp 'x')

    New-Item -ItemType Directory -Force $dir | Out-Null
    $exe = Join-Path $dir 'xf.exe'
    # A running xf.exe (the TUI, or an editor's xf lsp) can't be replaced,
    # but it can be renamed: move it aside and remove it once nothing runs it.
    Get-ChildItem -Path $dir -Filter 'xf.exe.old-*' | Remove-Item -ErrorAction SilentlyContinue
    if (Test-Path $exe) { Move-Item $exe "$exe.old-$(Get-Date -Format yyyyMMddHHmmss)" }
    Move-Item (Join-Path $tmp 'x\xf.exe') $exe
    Get-ChildItem -Path $dir -Filter 'xf.exe.old-*' | Remove-Item -ErrorAction SilentlyContinue
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

$path = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($path -split ';') -notcontains $dir) {
    [Environment]::SetEnvironmentVariable('Path', "$path;$dir", 'User')
    Write-Output "added $dir to your PATH; open a new terminal to run xf"
}
Write-Output "installed $exe"
