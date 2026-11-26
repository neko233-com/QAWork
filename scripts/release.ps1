param(
    [string]$Version = "",
    [switch]$SkipVerify
)
$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    if (-not $Version) {
        $versionFile = Join-Path $root "internal\version\version.go"
        $content = Get-Content $versionFile -Raw
        if ($content -match 'Version\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)"') {
            $Version = $Matches[1]
        } else {
            throw "无法从 internal/version/version.go 读取版本号"
        }
    }
    if ($Version -notmatch "^[0-9]+\.[0-9]+\.[0-9]+$") {
        throw "版本号必须形如 1.2.3"
    }
    $tag = "v$Version"
    if (-not $SkipVerify) {
        & (Join-Path $PSScriptRoot "verify.ps1")
        if ($LASTEXITCODE -ne 0) { throw "verify 失败，停止发布" }
    }

    $dist = Join-Path $root "dist\$tag"
    if (Test-Path $dist) { Remove-Item -Recurse -Force $dist }
    New-Item -ItemType Directory -Force -Path $dist | Out-Null

    $env:CGO_ENABLED = "0"
    go build -trimpath -ldflags "-s -w" -o (Join-Path $dist "qawork-windows-amd64.exe") .\cmd\qawork
    go build -trimpath -ldflags "-s -w" -o (Join-Path $dist "qawork-gui-windows-amd64.exe") .\cmd\qawork-gui

    $env:GOOS = "darwin"
    $env:GOARCH = "arm64"
    go build -trimpath -ldflags "-s -w" -o (Join-Path $dist "qawork-darwin-arm64") .\cmd\qawork
    go build -trimpath -ldflags "-s -w" -o (Join-Path $dist "qawork-gui-darwin-arm64") .\cmd\qawork-gui
    Remove-Item Env:GOOS, Env:GOARCH

    Compress-Archive -Path (Join-Path $dist "qawork-windows-amd64.exe"), (Join-Path $dist "qawork-gui-windows-amd64.exe") `
        -DestinationPath (Join-Path $dist "qawork-$tag-windows-amd64.zip") -Force
    Compress-Archive -Path (Join-Path $dist "qawork-darwin-arm64"), (Join-Path $dist "qawork-gui-darwin-arm64") `
        -DestinationPath (Join-Path $dist "qawork-$tag-darwin-arm64.zip") -Force

    if (-not (Get-Command gh -ErrorAction SilentlyContinue)) {
        throw "缺少 gh CLI；安装并 gh auth login 后再发布"
    }
    gh release create $tag `
        (Join-Path $dist "qawork-$tag-windows-amd64.zip") `
        (Join-Path $dist "qawork-$tag-darwin-arm64.zip") `
        --title "QAWork $tag" `
        --generate-notes
    Write-Host "release OK：$tag"
} finally {
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Remove-Item Env:GOOS -ErrorAction SilentlyContinue
    Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    Pop-Location
}
