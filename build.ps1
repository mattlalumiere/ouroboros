<#
.SYNOPSIS
  Builds the unpackers for every platform, embeds them into the packer, then
  builds the packer for every platform into dist\.

.DESCRIPTION
  Windows version of build.sh. Works in Windows PowerShell 5.1 and PowerShell 7.

.EXAMPLE
  .\build.ps1          # all platforms
  .\build.ps1 host     # only the packer for this machine
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('all', 'host')]
    [string]$Mode = 'all'
)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

$targets = @(
    'windows/amd64', 'windows/arm64',
    'linux/amd64', 'linux/arm64',
    'darwin/amd64', 'darwin/arm64'
)
$flags = @('-trimpath', '-buildvcs=false', '-ldflags', '-s -w')
$env:CGO_ENABLED = '0'

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'Go was not found. Install Go 1.26 or newer from https://go.dev/dl/ and open a new terminal.'
}

# Runs go with the arguments given and stops the script if it fails.
function Invoke-Go {
    param([string[]]$GoArgs)
    & go @GoArgs
    if ($LASTEXITCODE -ne 0) { throw "go $($GoArgs -join ' ') failed with exit code $LASTEXITCODE" }
}

# Builds for another system, then puts the environment back.
function Build-For {
    param([string]$Os, [string]$Arch, [string]$Output, [string]$Package)
    $env:GOOS = $Os
    $env:GOARCH = $Arch
    try {
        Invoke-Go (@('build') + $flags + @('-o', $Output, $Package))
    }
    finally {
        Remove-Item Env:GOOS -ErrorAction SilentlyContinue
        Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
    }
}

$stubDir = Join-Path 'internal' (Join-Path 'stubs' 'bin')
Get-ChildItem -LiteralPath $stubDir -Include 'unpacker-*.gz', 'MANIFEST' -ErrorAction SilentlyContinue |
    Remove-Item -Force
New-Item -ItemType Directory -Force -Path 'dist' | Out-Null

# Small helper that compresses and checksums the same way on every system.
$helper = Join-Path 'dist' 'stubpack.tmp.exe'
Invoke-Go @('build', '-o', $helper, './tools/stubpack')
$stub = Join-Path 'dist' 'stub.tmp'

Write-Host 'Building unpackers'
foreach ($t in $targets) {
    $os, $arch = $t -split '/'
    Build-For $os $arch $stub './cmd/unpacker'
    & $helper gz $stub (Join-Path $stubDir "unpacker-$os-$arch.gz")
    if ($LASTEXITCODE -ne 0) { throw "compressing the $t unpacker failed" }
}
Remove-Item -LiteralPath $stub -Force

# The packer checks the embedded unpackers against this list before using them.
& $helper manifest $stubDir
if ($LASTEXITCODE -ne 0) { throw 'writing the unpacker manifest failed' }
Remove-Item -LiteralPath $helper -Force

Write-Host 'Building packers'
if ($Mode -eq 'host') {
    $name = 'ouroboros'
    if ($env:OS -eq 'Windows_NT') { $name += '.exe' }
    Invoke-Go (@('build') + $flags + @('-o', (Join-Path 'dist' $name), './cmd/ouroboros'))
}
else {
    foreach ($t in $targets) {
        $os, $arch = $t -split '/'
        $ext = ''
        if ($os -eq 'windows') { $ext = '.exe' }
        Build-For $os $arch (Join-Path 'dist' "ouroboros-$os-$arch$ext") './cmd/ouroboros'
    }
}

Get-ChildItem -LiteralPath 'dist' |
    Select-Object Name, @{ Name = 'Size (MB)'; Expression = { [math]::Round($_.Length / 1MB, 1) } } |
    Format-Table -AutoSize
