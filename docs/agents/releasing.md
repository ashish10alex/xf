# Releasing xf

A release is a `v*` tag on `main`. The tag runs `.github/workflows/release.yml`: `go vet`, `go test`, then GoReleaser with `.goreleaser.yaml`, which publishes

- the archives for every OS and arch, with `checksums.txt`, as a GitHub Release (what `scripts/install.sh` and `scripts/install.ps1` download);
- the cask in `ashish10alex/homebrew-tap`;
- the manifest in `ashish10alex/scoop-bucket`;
- a pull request to `microsoft/winget-pkgs` from the fork `ashish10alex/winget-pkgs`, which Microsoft's checks and reviewers merge later.

## Tagging a release

1. **Check `main`.** Done when `just check` and `just release-check` pass, `just release-snapshot` writes every package into `dist/`, and the Windows workflow is green on `main`'s last commit.
2. **Tag it.** Ask the user for the version, then `git tag -a vX.Y.Z -m vX.Y.Z` on `main` and push the tag. Done when the release workflow is green, the GitHub Release has every archive and `checksums.txt`, the tap and the bucket have a commit for the version, and the winget pull request is open.

## When a release fails

- **Pushing to the tap, the bucket or the fork fails**: the `XF_PACKAGES_TOKEN` secret, a classic token with the `public_repo` scope (a fine-grained one can't open the pull request on `microsoft/winget-pkgs`), has expired. The user makes a new one and saves it; never handle the token.
- **Defender or SmartScreen flags `xf.exe`**: the Windows builds are unsigned. Run the release's zip on a Windows machine with real-time protection on, `xf serve --dir <project>` included (the engine copies itself into `%LocalAppData%\xf\bin`), and submit a flagged file to Microsoft as a false positive.
- **macOS refuses to open xf**: the Mac builds aren't notarized; the cask's postflight removes the quarantine attribute, and `install.sh` downloads with `curl`, which sets none.
