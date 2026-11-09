# Troubleshooting

- **`xf: not a Dataform or dbt project: … has none of workflow_settings.yaml, dataform.json, dbt_project.yml`**: run xf in the project's root (not a subdirectory such as `definitions/` or `models/`), or pass `--dir`.
- **`… is both a Dataform and a dbt project`**: pass `--backend dataform` or `--backend dbt`.
- **A dbt project shows `not compiled`**: you're on dbt v2 and the project has `on-run-start` or `on-run-end` hooks; see [dbt projects](dbt.md#dbt-projects).
- **Dry runs say to log in**: run `gcloud auth application-default login` (step 2).
- **`xf impact` in CI says `neither origin/main nor main exists`, or finds no common ancestor**: the job fetched too little history; fetch the default branch (`fetch-depth: 0`).
- **Column impact says "no access to Data Lineage"**: enable the Data Lineage API in the tables' project and ask for `roles/datalineage.viewer`.
  With user credentials, the quota project in your ADC file (`gcloud auth application-default set-quota-project`) needs the API enabled too.
- **No `xf` client in `:checkhealth vim.lsp`**: check that `xf` is on the `PATH` neovim sees (`:!which xf`; on Windows, `Get-Command xf` in PowerShell), and that the buffer is inside a directory with `workflow_settings.yaml` (or `dbt_project.yml`).
- **"the xf on PATH is older than this plugin"**: update xf as you installed it ([Install](../README.md#install)), then run `:lsp restart`.
- **Copying (`y`) does nothing on Linux**: install `xclip`, `xsel` or `wl-clipboard`.
- **The engine misbehaves**: it logs to `xf-<hash>-<tool>-<variant>-v<n>.log` in `$TMPDIR` (or `/tmp`), next to its socket; on Windows, in `%TEMP%` (`Get-ChildItem $env:TEMP\xf-*.log`).
  `xf stop` stops every engine you run, and the next `xf` starts a fresh one.
- **Windows: upgrading fails with "Access is denied"**, or `just install` can't remove an old `xf.exe.old-…`: an xf is still running.
  Run `xf stop` and close what it lists ([Upgrading](windows.md#upgrading)).
- **Windows: `xf` isn't recognised**: its folder isn't on your `PATH` (`$env:Path -split ';'` lists it), or the terminal was opened before you added it.
- **Windows: `xf.exe` vanished**: Windows Defender may have quarantined it ([Running a downloaded `xf.exe`](windows.md#running-a-downloaded-xfexe)).
