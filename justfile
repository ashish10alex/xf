# Plain recipes run in PowerShell on Windows, which has no sh of its own
set windows-shell := ["powershell.exe", "-NoLogo", "-NoProfile", "-Command"]

golangci_lint := "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0"
# Run with go run, as CI's goreleaser-action pins the same version
goreleaser := "github.com/goreleaser/goreleaser/v2@v2.18.2"
# A real Dataform project for the local smoke test; kept out of the repo.
local_project := env_var_or_default("XF_LOCAL_PROJECT", "")

# Build the xf binary into bin/
build:
    go build -o bin/xf ./cmd/xf

# Install xf into $GOBIN (or ~/go/bin) so editors can start `xf lsp`; on a Mac with the launchd agent
# (scripts/launchd/xf-web-agent.sh) it restarts xf web on the new binary, which a running one doesn't pick up
[unix]
install:
    go install ./cmd/xf
    @if [ "$(uname)" = Darwin ] && launchctl print "gui/$(id -u)/dev.xf.web" >/dev/null 2>&1; then launchctl kickstart -k "gui/$(id -u)/dev.xf.web" && echo "restarted xf web (dev.xf.web) on the new xf"; fi

# Install xf into $GOBIN (or ~/go/bin), moving a running xf.exe aside first
[windows]
install:
    powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-install.ps1

# Run xf from the current directory, passing any flags through
run *args:
    go run ./cmd/xf {{args}}

test:
    go test ./...

# go vet + go test here; what pushing main runs (see .githooks/pre-push)
check:
    go vet ./...
    go test ./...

# Ship committed work from the main checkout: rebase on origin/main, check that commit in a clean copy, push main,
# fast-forward this checkout, install xf, remove the branch's worktree (see scripts/ship.sh)
[unix]
ship branch="":
    sh scripts/ship.sh {{branch}}

# Go image `just check-linux` runs in; matches go.mod's go line
linux_image := "golang:1.26"

# go vet + go test in a Linux container, for changes that may behave differently there
check-linux:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! docker info >/dev/null 2>&1; then
        echo "check-linux: Docker isn't running; start Docker Desktop and try again." >&2
        exit 1
    fi
    echo "== linux (in {{linux_image}}): go vet + go test"
    # -buildvcs=false: a git worktree's .git points outside the mount
    docker run --rm -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false \
        -v xf-check-gomod:/go/pkg/mod -v xf-check-gocache:/root/.cache/go-build \
        {{linux_image}} sh -c 'git config --global --add safe.directory /src && go vet ./... && go test ./...'

# Use the repo's git hooks (.githooks), so pushing main runs `just check`
install-hooks:
    git config core.hooksPath .githooks

# Build every OS and arch into dist/ with GoReleaser, as CI does, publishing nothing
release-snapshot:
    go run {{goreleaser}} release --snapshot --clean

# Check .goreleaser.yaml
release-check:
    go run {{goreleaser}} check

# Pack the VS Code test extension for a VS Code target (win32-x64, win32-arm64, darwin-arm64, ...) with xf built for it
vsix target="win32-x64":
    #!/usr/bin/env bash
    set -euo pipefail
    target="{{target}}"
    target="${target#target=}" # just vsix target=win32-arm64 works too
    case "$target" in
        win32-x64) os=windows arch=amd64 ;;
        win32-arm64) os=windows arch=arm64 ;;
        darwin-x64) os=darwin arch=amd64 ;;
        darwin-arm64) os=darwin arch=arm64 ;;
        linux-x64) os=linux arch=amd64 ;;
        linux-arm64) os=linux arch=arm64 ;;
        *) echo "vsix: unknown target $target" >&2; exit 1 ;;
    esac
    exe=xf
    if [ "$os" = windows ]; then exe=xf.exe; fi
    rm -rf editors/vscode/bin
    CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "editors/vscode/bin/$exe" ./cmd/xf
    cd editors/vscode
    npm ci --no-audit --no-fund
    npx --yes @vscode/vsce@4.0.0 package --target "$target" --skip-license -o "xf-$target.vsix"

# go vet plus golangci-lint v2 (via go run, so it matches the module's Go version)
lint:
    go vet ./...
    go run {{golangci_lint}} run ./...

# Regenerate committed compile fixtures from the tiny projects in testdata/projects
fixtures:
    #!/usr/bin/env bash
    set -euo pipefail
    for p in testdata/projects/*/; do
        name=$(basename "$p")
        (cd "$p" && dataform compile --json) > "testdata/${name}.json" || true
        echo "wrote testdata/compile_${name}.json"
    done

# Regenerate the compile fixtures of the example projects: Dataform's JSON, and
# trimmed dbt manifests from dbt on PATH (v2) and $XF_DBT_CORE_BIN (dbt-core 1.x)
fixtures-examples:
    #!/usr/bin/env bash
    set -euo pipefail
    (cd examples/dataform && dataform compile --json) > testdata/examples/dataform.json
    (cd examples/dataform-broken && dataform compile --json) > testdata/examples/dataform-broken.json || true
    go test ./internal/backend/dbt -run TestUpdateFixtures -update -v

# Load the example projects' raw tables into BigQuery (raw dataset, EU) with the bq CLI
load-examples project="alex-personal-dev-01":
    examples/data/load.sh {{project}}

# Save a real project's compile output (gitignored) for the local smoke test; set $XF_LOCAL_PROJECT to its root
fixtures-local:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -z "{{local_project}}" ]; then
        echo 'set XF_LOCAL_PROJECT to the root of a real Dataform project' >&2
        exit 1
    fi
    mkdir -p testdata/local
    cd "{{local_project}}" && dataform compile --json > "{{justfile_directory()}}/testdata/local/project.json"

# Copy BigQuery's functions (the catalog and the borrowed snippets) from vscode-dataform-tools
[unix]
bigquery-functions from="~/vscode-dataform-tools":
    cp {{from}}/snippets/bigquery-functions.json {{from}}/snippets/bigquery.code-snippets.json internal/bqfuncs/
    go test ./internal/bqfuncs/
