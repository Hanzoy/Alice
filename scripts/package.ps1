param(
    [string]$Version = "0.2.1",
    [switch]$WithoutToolchain
)

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $PSScriptRoot
$GoWrapper = Join-Path $PSScriptRoot "go.ps1"
$DistRoot = Join-Path $ProjectRoot "dist"
$Suffix = if ($WithoutToolchain) { "windows-amd64" } else { "windows-amd64-portable" }
$PackageName = "Alice-v$Version-$Suffix"
$Stage = Join-Path $DistRoot $PackageName
$Archive = Join-Path $DistRoot "$PackageName.zip"

New-Item -ItemType Directory -Path $DistRoot -Force | Out-Null
if (Test-Path $Stage) {
    Remove-Item -LiteralPath $Stage -Recurse -Force
}
if (Test-Path $Archive) {
    Remove-Item -LiteralPath $Archive -Force
}
New-Item -ItemType Directory -Path $Stage -Force | Out-Null

& $GoWrapper build -trimpath -ldflags "-s -w" -o (Join-Path $Stage "alice.exe") ./cmd/alice
if ($LASTEXITCODE -ne 0) {
    throw "Alice build failed"
}

Copy-Item -LiteralPath (Join-Path $ProjectRoot "README.md") -Destination $Stage
Copy-Item -LiteralPath (Join-Path $ProjectRoot "go.mod") -Destination $Stage
Copy-Item -LiteralPath (Join-Path $ProjectRoot "go.sum") -Destination $Stage
Copy-Item -LiteralPath (Join-Path $ProjectRoot "Dockerfile") -Destination $Stage
Copy-Item -LiteralPath (Join-Path $ProjectRoot "compose.yaml") -Destination $Stage
Copy-Item -LiteralPath (Join-Path $ProjectRoot ".env.example") -Destination $Stage
Copy-Item -LiteralPath (Join-Path $ProjectRoot "start-alice.cmd") -Destination $Stage
Copy-Item -LiteralPath (Join-Path $ProjectRoot "start-alice.ps1") -Destination $Stage
Copy-Item -LiteralPath (Join-Path $ProjectRoot "start-docker.cmd") -Destination $Stage
Copy-Item -LiteralPath (Join-Path $ProjectRoot "start-docker.ps1") -Destination $Stage
Copy-Item -LiteralPath (Join-Path $ProjectRoot "docs") -Destination $Stage -Recurse
Copy-Item -LiteralPath (Join-Path $ProjectRoot "components") -Destination $Stage -Recurse
Copy-Item -LiteralPath (Join-Path $ProjectRoot "cmd") -Destination $Stage -Recurse
Copy-Item -LiteralPath (Join-Path $ProjectRoot "internal") -Destination $Stage -Recurse
New-Item -ItemType Directory -Path (Join-Path $Stage "pkg") -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $ProjectRoot "pkg\component") -Destination (Join-Path $Stage "pkg") -Recurse
New-Item -ItemType Directory -Path (Join-Path $Stage "scripts") -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $ProjectRoot "scripts\bootstrap-go.ps1") -Destination (Join-Path $Stage "scripts")
Copy-Item -LiteralPath (Join-Path $ProjectRoot "scripts\go.ps1") -Destination (Join-Path $Stage "scripts")

if (-not $WithoutToolchain) {
    $GoRoot = Join-Path $ProjectRoot ".tools\go"
    if (-not (Test-Path (Join-Path $GoRoot "bin\go.exe"))) {
        throw "Project-local Go toolchain is missing. Run scripts\bootstrap-go.ps1 first or package with -WithoutToolchain."
    }
    New-Item -ItemType Directory -Path (Join-Path $Stage ".tools") -Force | Out-Null
    Copy-Item -LiteralPath $GoRoot -Destination (Join-Path $Stage ".tools") -Recurse
}

$Tar = Get-Command tar.exe -ErrorAction SilentlyContinue
if ($null -ne $Tar) {
    & $Tar.Source -a -c -f $Archive -C $DistRoot $PackageName
    if ($LASTEXITCODE -ne 0) {
        throw "Archive creation failed"
    }
} else {
    Compress-Archive -LiteralPath $Stage -DestinationPath $Archive -CompressionLevel Optimal
}

$Hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $Archive).Hash.ToLowerInvariant()
$Size = (Get-Item -LiteralPath $Archive).Length
Write-Host "Package: $Archive"
Write-Host "Size: $Size bytes"
Write-Host "SHA256: $Hash"
