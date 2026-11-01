# Installs xf into $GOBIN (or GOPATH\bin) on Windows, as `go install` does.
# A running xf.exe (the TUI, or an editor's xf lsp) can't be replaced, but it
# can be renamed: the new build is made first, then the old one is moved
# aside and removed once nothing runs it.
$ErrorActionPreference = 'Stop'
$bin = go env GOBIN
if (-not $bin) { $bin = Join-Path (go env GOPATH) 'bin' }
New-Item -ItemType Directory -Force $bin | Out-Null
$exe = Join-Path $bin 'xf.exe'
$new = "$exe.new"
go build -o $new ./cmd/xf
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
# Copies an earlier install moved aside, if they have stopped running.
Get-ChildItem -Path $bin -Filter 'xf.exe.old-*' | Remove-Item -ErrorAction SilentlyContinue
if (Test-Path $exe) {
    Move-Item $exe "$exe.old-$(Get-Date -Format yyyyMMddHHmmss)"
}
Move-Item $new $exe
Get-ChildItem -Path $bin -Filter 'xf.exe.old-*' | Remove-Item -ErrorAction SilentlyContinue
Write-Output "installed $exe"
