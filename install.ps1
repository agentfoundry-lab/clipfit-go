[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$BinaryPath,

    [string]$InstallDir
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-ClipFitGoArch {
    if (-not [string]::IsNullOrWhiteSpace($env:CLIPFIT_GOARCH)) {
        return $env:CLIPFIT_GOARCH
    }

    $architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    switch ($architecture) {
        "X64" { return "amd64" }
        "Arm64" { return "arm64" }
        "X86" { return "386" }
        default { throw "Unsupported Windows architecture: $architecture" }
    }
}

if ([string]::IsNullOrWhiteSpace($BinaryPath)) {
    $goArch = Get-ClipFitGoArch
    $BinaryPath = Join-Path $PSScriptRoot "dist\windows-$goArch\clipfit.exe"
}

if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    if (-not [string]::IsNullOrWhiteSpace($env:CLIPFIT_INSTALL_DIR)) {
        $InstallDir = $env:CLIPFIT_INSTALL_DIR
    }
    elseif (-not [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        $InstallDir = Join-Path $env:LOCALAPPDATA "Programs\ClipFit"
    }
    else {
        throw "LOCALAPPDATA is unavailable; pass -InstallDir explicitly."
    }
}

if (-not (Test-Path -LiteralPath $BinaryPath -PathType Leaf)) {
    throw "Compiled Windows binary not found: $BinaryPath. Run build.sh first."
}

$source = (Get-Item -LiteralPath $BinaryPath).FullName
$null = New-Item -ItemType Directory -Path $InstallDir -Force
$target = Join-Path $InstallDir "clipfit.exe"

if ($source -ne [System.IO.Path]::GetFullPath($target)) {
    Copy-Item -LiteralPath $source -Destination $target -Force
}

Write-Output "Installed clipfit to $target"

$pathEntries = $env:Path -split [System.IO.Path]::PathSeparator
if ($pathEntries -notcontains $InstallDir) {
    Write-Warning "Add $InstallDir to PATH to run clipfit.exe directly."
}
