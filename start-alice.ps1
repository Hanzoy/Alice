$ErrorActionPreference = "Stop"
$Root = $PSScriptRoot
$Executable = Join-Path $Root "alice.exe"
if (-not (Test-Path $Executable)) {
    $Executable = Join-Path $Root "bin\alice.exe"
}
if (-not (Test-Path $Executable)) {
    throw "alice.exe not found. Build or unpack the release package first."
}

Set-Location $Root
Write-Host "Alice Core: http://localhost:8080"
& $Executable -addr ":8080" -data (Join-Path $Root "data") -components (Join-Path $Root "components")

