$ErrorActionPreference = 'Stop'

$source = Join-Path $PSScriptRoot 'wailsjs'
$target = Join-Path $PSScriptRoot 'dist/wailsjs'
$resolvedRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'dist'))
$resolvedTarget = [IO.Path]::GetFullPath($target)
if (-not $resolvedTarget.StartsWith($resolvedRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw 'Binding destination is outside the frontend output directory' }

if (-not (Test-Path $source)) {
  throw "Wails bindings not found: $source"
}

if (-not (Test-Path (Join-Path $PSScriptRoot 'dist'))) {
  New-Item -ItemType Directory -Force -Path (Join-Path $PSScriptRoot 'dist') | Out-Null
}

Remove-Item -Recurse -Force $target -ErrorAction SilentlyContinue
Copy-Item -Recurse $source $target
