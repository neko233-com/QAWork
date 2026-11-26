$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    if (Test-Path ".github\workflows") {
        throw "禁止 GitHub Actions：请删除 .github\workflows，只允许本地 scripts/verify.ps1 与 scripts/release.ps1"
    }
    $env:CGO_ENABLED = "0"
    if ((go env CGO_ENABLED).Trim() -ne "0") {
        throw "CGO_ENABLED 必须是 0"
    }
    $unformatted = gofmt -l .
    if ($unformatted) {
        throw "gofmt 未通过：`n$unformatted"
    }
    go vet ./...
    go test -count=1 ./...

    $dist = Join-Path $root "dist\verify"
    New-Item -ItemType Directory -Force -Path $dist | Out-Null
    go build -o (Join-Path $dist "qawork.exe") .\cmd\qawork
    go build -o (Join-Path $dist "qawork-gui.exe") .\cmd\qawork-gui

    $env:GOOS = "darwin"
    $env:GOARCH = "arm64"
    go build -o (Join-Path $dist "qawork-darwin-arm64") .\cmd\qawork
    go build -o (Join-Path $dist "qawork-gui-darwin-arm64") .\cmd\qawork-gui
    Remove-Item Env:GOOS, Env:GOARCH
    Write-Host "verify OK：Windows amd64 构建 + macOS arm64 交叉编译 + 全部单测"
} finally {
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Pop-Location
}
