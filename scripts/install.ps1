# Installs the latest xf release on Windows:
#
#   irm https://raw.githubusercontent.com/ashish10alex/xf/main/scripts/install.ps1 | iex
#
# $env:XF_VERSION picks a release (e.g. v0.0.1) instead of the latest, and
# $env:XF_INSTALL_DIR the folder xf.exe goes in (default
# %LocalAppData%\Programs\xf), which is added to the user PATH. The zip is
# checked against the release's checksums.txt before anything is installed.
# To build from source instead, see scripts/build-install.ps1.
#
# Everything runs inside Install-Xf, so the preferences it sets stay out of
# the caller's session, and a failure prints a message instead of a stack:
# `iex` runs this in the user's own PowerShell, which `exit` would close.

function Install-Xf {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue' # Invoke-WebRequest is slow with it
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

    $repo = 'ashish10alex/xf'
    $releases = "https://github.com/$repo/releases"
    $help = "https://github.com/$repo/blob/main/docs/windows.md#behind-a-company-proxy"
    $dir = if ($env:XF_INSTALL_DIR) { $env:XF_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\xf' }

    # The OS's architecture, not this PowerShell's: x64 PowerShell on ARM64
    # Windows would otherwise pick the emulated build.
    $arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
        'X64' { 'amd64' }
        'Arm64' { 'arm64' }
        default { throw "xf: unsupported architecture $_" }
    }
    $base = if ($env:XF_VERSION) { "$releases/download/$env:XF_VERSION" } else { "$releases/latest/download" }
    $zip = "xf_windows_$arch.zip"

    $proxyPage = @"
xf: downloading $zip returned a web page instead of the file, so nothing was installed.
A company proxy or firewall (such as Zscaler) is probably scanning or blocking the download.
  - If it is scanning the file, wait about 10 minutes, then run the install command again.
  - Or download $zip and checksums.txt in your browser from $releases/latest
    and install them by hand: $help
"@

    # Downloads $url to $out. A proxy that holds or blocks the file answers
    # with its own HTML page, as an HTTP error or, for some, as a 200 in
    # place of the file, so both get the same message.
    function Get-File($url, $out) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $out
        } catch {
            # With -OutFile, Windows PowerShell leaves ErrorDetails empty, so
            # the page is also told by its content type: HttpWebResponse's
            # ContentType there, HttpResponseMessage's headers in PowerShell 7.
            $body = "$($_.ErrorDetails.Message)"
            $status = $null
            $type = ''
            $resp = $_.Exception.Response
            if ($resp) {
                $status = [int]$resp.StatusCode
                if ($resp.PSObject.Properties['ContentType']) { $type = "$($resp.ContentType)" }
                elseif ($resp.PSObject.Properties['Content'] -and $resp.Content) { $type = "$($resp.Content.Headers.ContentType)" }
            }
            if ($body -match '<html|<!doctype' -or $type -match 'html') { throw $proxyPage }
            if ($status -eq 404 -and $env:XF_VERSION) { throw "xf: release $env:XF_VERSION has no $(Split-Path $url -Leaf); check `$env:XF_VERSION against $releases. Nothing was installed." }
            if ($status) { throw "xf: downloading $url failed with HTTP $status. Nothing was installed." }
            throw "xf: couldn't download $url ($($_.Exception.Message)). Check your internet connection or proxy settings. Nothing was installed."
        }
        $fs = [System.IO.File]::OpenRead($out)
        try { $buf = New-Object byte[] 64; $n = $fs.Read($buf, 0, $buf.Length) } finally { $fs.Close() }
        if ([System.Text.Encoding]::ASCII.GetString($buf, 0, $n).TrimStart().StartsWith('<')) { throw $proxyPage }
    }

    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("xf-install-" + [guid]::NewGuid())
    New-Item -ItemType Directory $tmp | Out-Null
    try {
        Get-File "$base/$zip" (Join-Path $tmp $zip)
        Get-File "$base/checksums.txt" (Join-Path $tmp 'checksums.txt')
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
}

try {
    Install-Xf
} catch {
    Write-Host $_.Exception.Message -ForegroundColor Red
    $global:LASTEXITCODE = 1
}
