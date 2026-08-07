param(
    [string]$Version = "1.26.5",
    [string]$Sha256 = "97e6b2a833b6d89f9ff17d25419ac0a7e3b482a044e9ab18cdef834bd834fd38"
)

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $PSScriptRoot
$ToolRoot = Join-Path $ProjectRoot ".tools"
$Archive = Join-Path $ToolRoot "go$Version.windows-amd64.zip"
$GoExe = Join-Path $ToolRoot "go\bin\go.exe"

if (Test-Path $GoExe) {
    & $GoExe version
    exit 0
}

New-Item -ItemType Directory -Path $ToolRoot -Force | Out-Null
Invoke-WebRequest -Uri "https://go.dev/dl/go$Version.windows-amd64.zip" -OutFile $Archive
$Actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $Archive).Hash.ToLowerInvariant()
if ($Actual -ne $Sha256.ToLowerInvariant()) {
    throw "Go archive checksum mismatch: expected $Sha256, got $Actual"
}
Expand-Archive -LiteralPath $Archive -DestinationPath $ToolRoot -Force
& $GoExe version

