$ErrorActionPreference = "Stop"
$Root = $PSScriptRoot
Set-Location $Root
if (-not (Test-Path ".env") -and (Test-Path ".env.example")) {
    Copy-Item -LiteralPath ".env.example" -Destination ".env"
    Write-Warning "已创建 .env。正式使用前请修改其中三个密钥。"
}
docker compose up -d --build
if ($LASTEXITCODE -ne 0) { throw "Alice Docker 启动失败" }
Write-Host "Alice Core: http://localhost:8080"
docker compose ps
