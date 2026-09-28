param(
    [string]$EnvFile = '.env.r2',
    [switch]$CheckOnly
)
$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$resolvedEnvFile = if ([IO.Path]::IsPathRooted($EnvFile)) { $EnvFile } else { Join-Path $repoRoot $EnvFile }
if (-not (Test-Path -LiteralPath $resolvedEnvFile -PathType Leaf)) { throw 'Create .env.r2 from .env.r2.example first.' }

# Parse only literal allowlisted fields. Never dot-source or execute an env file.
$allowed = @('R2_ACCOUNT_ID','R2_BUCKET','R2_ACCESS_KEY_ID','R2_SECRET_ACCESS_KEY','R2_WORKER_ACCESS_KEY_ID','R2_WORKER_SECRET_ACCESS_KEY','R2_JURISDICTION')
$settings = @{}
foreach ($line in Get-Content -LiteralPath $resolvedEnvFile) {
    if ($line -match '^\s*([A-Z0-9_]+)\s*=(.*)$' -and $Matches[1] -in $allowed) {
        $settings[$Matches[1]] = $Matches[2].Trim().Trim('"').Trim("'")
    }
}
foreach ($name in @('R2_ACCOUNT_ID','R2_BUCKET','R2_ACCESS_KEY_ID','R2_SECRET_ACCESS_KEY','R2_WORKER_ACCESS_KEY_ID','R2_WORKER_SECRET_ACCESS_KEY')) {
    if ([string]::IsNullOrWhiteSpace($settings[$name])) { throw "Missing $name in the private configuration file." }
}
if ($settings['R2_ACCOUNT_ID'] -notmatch '^[0-9a-fA-F]{32}$') { throw 'R2_ACCOUNT_ID must be 32 hexadecimal characters.' }
if ($settings['R2_JURISDICTION'] -and $settings['R2_JURISDICTION'] -notin @('eu')) { throw 'R2_JURISDICTION must be empty or eu.' }
if ($CheckOnly) { Write-Output 'Required R2 configuration is present. No network calls or uploads made.'; return }

$previous = @{}
$names = $allowed + @('STORAGE_MODE','GAMICS_TEST_STORAGE','GOCACHE')
foreach ($name in $names) { $previous[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
try {
    foreach ($name in $allowed) { [Environment]::SetEnvironmentVariable($name, $settings[$name], 'Process') }
    $env:STORAGE_MODE = 'r2'
    $env:GAMICS_TEST_STORAGE = '1'
    $env:GOCACHE = Join-Path $repoRoot '.gocache'
    Write-Output 'Checking R2 with one small private test object. The object remains under conformance/.'
    & go -C (Join-Path $repoRoot 'services/api') test ./internal/storage -run '^TestS3LiveUploadConformance$' -count=1 -v
    if ($LASTEXITCODE -ne 0) { throw 'R2 conformance failed. Do not enable player uploads until it passes.' }
} finally {
    foreach ($name in $names) { [Environment]::SetEnvironmentVariable($name, $previous[$name], 'Process') }
}
