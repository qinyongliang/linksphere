param(
  [string]$Version = 'dev',
  [ValidateSet('win-x64', 'win-x86', 'win-arm64')]
  [string]$Runtime = 'win-x64',
  [switch]$SkipWebBuild
)

$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^[A-Za-z0-9][A-Za-z0-9._+-]*$') { throw 'Invalid version label' }
$workspaceRoot = [IO.Path]::GetFullPath($PSScriptRoot)

function Require-Command {
  param([string]$Name, [string]$Message)
  $cmd = Get-Command $Name -ErrorAction SilentlyContinue
  if (-not $cmd) {
    throw $Message
  }
  return $cmd.Source
}

$go = Require-Command go 'go is required to build gosshd-server.exe.'
$wailsCmd = Get-Command wails -ErrorAction SilentlyContinue
if ($wailsCmd) {
  $wailsPath = $wailsCmd.Source
} else {
  Write-Host 'wails CLI not found; installing github.com/wailsapp/wails/v2/cmd/wails@v2.12.0'
  if (-not $env:GOPROXY) {
    $env:GOPROXY = 'https://goproxy.cn,direct'
  }
  & $go install github.com/wailsapp/wails/v2/cmd/wails@v2.12.0
  $goBin = Join-Path (go env GOPATH) 'bin'
  $wailsPath = Join-Path $goBin 'wails.exe'
  if (-not (Test-Path $wailsPath)) {
    throw 'wails CLI is required to build the desktop client, and automatic installation did not produce wails.exe.'
  }
}

if (-not $SkipWebBuild) {
  $pnpm = Require-Command pnpm 'pnpm is required to build the embedded React assets. Pass -SkipWebBuild only if dist/ is already current.'
  & $pnpm install --frozen-lockfile
  & $pnpm run build
}

$packageBase = Join-Path $workspaceRoot 'package'
$packageRoot = [IO.Path]::GetFullPath((Join-Path $packageBase "gosshd-client-$Runtime"))
if (-not $packageRoot.StartsWith($packageBase + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw 'Package directory is outside the workspace package root' }
Remove-Item -Recurse -Force $packageRoot -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $packageRoot | Out-Null

$wailsPlatform = switch ($Runtime) {
  'win-arm64' { 'windows/arm64' }
  'win-x86' { 'windows/386' }
  default { 'windows/amd64' }
}

Push-Location client/wails
try {
  & $wailsPath build -platform $wailsPlatform -trimpath -o GOSSHD.exe
  if ($LASTEXITCODE -ne 0) { throw 'Wails client build failed' }
} finally {
  Pop-Location
}

$wailsOutput = Join-Path 'client/wails/build/bin' 'GOSSHD.exe'
if (-not (Test-Path $wailsOutput)) {
  throw "Wails build completed but $wailsOutput was not found."
}
Copy-Item $wailsOutput (Join-Path $packageRoot 'GOSSHD.exe') -Force

Copy-Item client/wails/README.md (Join-Path $packageRoot 'README-client.md') -Force

$zip = Join-Path 'package' "gosshd-client-$Runtime-$Version.zip"
Remove-Item -Force $zip -ErrorAction SilentlyContinue
Compress-Archive -Path (Join-Path $packageRoot '*') -DestinationPath $zip
Write-Host "client package ready: $zip"
