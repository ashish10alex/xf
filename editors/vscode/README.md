# xf (test)

A test-only VS Code extension for xf. It is never published. It proves the
editor experience, on Windows above all:

- `xf lsp` for each Dataform project (`.sqlx` and JavaScript files) and dbt
  project (`.sql`) in the workspace: definitions, hovers, completion and
  diagnostics. Its logs are in the **xf** output channel.
- **xf: Open** runs the TUI in a terminal beside the editor. It follows the
  active editor, and its `o` key opens files back in VS Code.

xf comes bundled; set `xf.path` to test another build. The `dataform` and
`dbt` binaries and the dbt target come from xf's own config.

Build it from the repo root with `just vsix target=<target>` (`win32-x64`,
`win32-arm64`, `darwin-arm64`, `darwin-x64`, `linux-x64` or `linux-arm64`),
then `code --install-extension editors/vscode/xf-<target>.vsix`. Disable
Dataform Tools while testing, so two language servers don't answer.

Keep the Python Environments extension (`ms-python.vscode-python-envs`) up to
date. Version 1.10.0 types the venv's activation command into every terminal
open while it starts, the xf terminal too, and xf reads the text as keys: it
can start a run. Version 1.38.0 leaves the xf terminal alone.
