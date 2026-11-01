# xf on Windows

xf runs natively on Windows 10 22H2 and Windows 11, x64 and ARM64, from PowerShell or cmd. Use **Windows Terminal** or **VS Code's integrated terminal**; the old console host (conhost) isn't supported. Everything above works the same, except `xf --nvim` (see [neovim on Windows](#neovim-on-windows)).

## Install

With winget (`winget upgrade AshishAlex.xf` updates it):

```powershell
winget install AshishAlex.xf
```

With [Scoop](https://scoop.sh) (`scoop update xf` updates it):

```powershell
scoop bucket add ashish10alex https://github.com/ashish10alex/scoop-bucket
scoop install xf
```

Or with the install script, which puts the latest release in `%LocalAppData%\Programs\xf` (`$env:XF_INSTALL_DIR` picks another folder) after checking it against the release's checksums, and adds that folder to your `PATH`; run it again to update:

```powershell
irm https://raw.githubusercontent.com/ashish10alex/xf/main/scripts/install.ps1 | iex
```

Each picks the x64 or ARM64 build for your Windows. The [releases](https://github.com/ashish10alex/xf/releases) also have the zips, `xf_windows_amd64.zip` (x64) and `xf_windows_arm64.zip` (ARM64), each holding a single portable `xf.exe`. Open a new terminal (a `PATH` change applies to new ones) and check it:

```powershell
xf --help
```

To build from source instead, install Go and [`just`](https://github.com/casey/just) (`winget install Casey.Just`), clone the repo and run `just install`. It installs into `$(go env GOPATH)\bin`; add that to your `PATH` as above. Use `just install` rather than `go install ./cmd/xf` to update: Windows can't overwrite an `xf.exe` that's running (a TUI or an editor's `xf lsp`), so `just install` builds first, then moves the running one aside.

## Running a downloaded `xf.exe`

xf isn't signed. Run from a terminal, it starts without a prompt. If you double-click a downloaded `xf.exe`, or a zip extracted from a browser download, SmartScreen may say "Windows protected your PC": choose **More info**, then **Run anyway**. Or clear the download mark before extracting:

```powershell
Unblock-File .\xf_windows_amd64.zip
```

Windows Defender has in the past flagged unsigned xf builds as a trojan, a false positive. If it removes `xf.exe`, restore it from **Windows Security → Protection history**, and report the file to Microsoft as a false positive at [microsoft.com/wdsi/filesubmission](https://www.microsoft.com/wdsi/filesubmission), so the next build isn't flagged for anyone.

## Upgrading

xf's background engine runs from its own copy of xf (in `%LocalAppData%\xf\bin`), so it doesn't stop you replacing `xf.exe`. The TUI and editors' language servers run `xf.exe` itself, though, and Windows won't replace a file in use. Before upgrading, run:

```powershell
xf stop
```

It stops your engines and lists the xf processes still running, with the app each runs in (Windows Terminal, VS Code, nvim). Quit xf in those terminals and close those editor windows, then upgrade. If `winget upgrade` reports **Access is denied**, your old xf still works: run `xf stop`, close what it lists, and try again. After an upgrade, the first xf of the new build replaces any engine left from the old one.

## Tools, settings and logs

- **Dataform**: xf finds the `dataform` that `npm i -g @dataform/cli` installs (a `dataform.cmd` shim) on your `PATH`.
- **dbt**: xf uses the dbt in the project's virtualenv (`.venv\Scripts\dbt.exe` or `venv\Scripts\dbt.exe`) before the `dbt` on your `PATH`, on every OS, so a project's dbt-core is used even when another dbt (such as dbt v2) is on `PATH`. Create one with `python -m venv .venv`, then `.venv\Scripts\pip install dbt-core dbt-bigquery`. `--dbt-bin`, `$DBT_BIN` or `"dbtBin"` still win.
- **Settings**: your user config is `%AppData%\xf\config.json` (or under `$XDG_CONFIG_HOME` if you set it), and `~\` in config paths means your home folder, as `~/` does.
- **Caches and logs**: compiles and dbt artifacts are cached in `%LocalAppData%\xf`. The engine logs to `xf-<hash>-<tool>-<variant>-v<n>.log` in `%TEMP%`.
- **Long paths**: dbt and dbt v2 can fail on paths longer than Windows' 260-character limit, so keep projects at a short path, such as under `C:\src`. xf's own snapshots and caches don't need long-path support.
- **Copying the run's command**: the command line the Run pane shows pastes into PowerShell and cmd as it is.

## neovim on Windows

The language server works as on macOS: install the plugin as in [step 4](neovim.md#permanent-install-the-plugin) and `vim.lsp.enable("xf")`. `xf --nvim` and the `:Xf` split aren't supported on Windows; run xf in a Windows Terminal pane beside neovim instead. It follows neovim's buffers, and `o` opens files in the neovim you used last.

## VS Code (testing only)

`editors/vscode` is a test-only VS Code extension that runs `xf lsp` in Dataform and dbt projects and opens the TUI beside the editor (**xf: Open**), where `o` opens files back in VS Code. It isn't published; build it with `just vsix target=win32-x64` (or `win32-arm64`) and install it with `code --install-extension editors/vscode/xf-win32-x64.vsix`. Disable Dataform Tools while you use it, so two language servers don't answer. See [its README](../editors/vscode/README.md).
