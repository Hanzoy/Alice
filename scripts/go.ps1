$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $PSScriptRoot
$GoExe = Join-Path $ProjectRoot ".tools\go\bin\go.exe"
if (-not (Test-Path $GoExe)) {
    $Command = Get-Command go -ErrorAction SilentlyContinue
    if ($null -eq $Command) {
        throw "Go toolchain not found. Run .\scripts\bootstrap-go.ps1 first."
    }
    $GoExe = $Command.Source
}

$env:GOCACHE = Join-Path $ProjectRoot ".cache\go-build"
$env:GOPATH = Join-Path $ProjectRoot ".cache\gopath"
$env:GOMODCACHE = Join-Path $ProjectRoot ".cache\gomod"
$env:GOTOOLCHAIN = "local"
& $GoExe @args
exit $LASTEXITCODE
