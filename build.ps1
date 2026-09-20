# Builds the application, agent first.
#
# The agent is the half that runs on the phone and it is embedded into the
# application, so building it second would ship the previous one — and the
# phone would keep reading tags with old code.

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$agent = 'internal/device/agent/mlm-agent-arm64'

Write-Host 'Building the phone agent...'
$env:CGO_ENABLED = '0'
$env:GOOS = 'linux'
$env:GOARCH = 'arm64'
go build -trimpath -ldflags='-s -w' -o $agent ./cmd/agent
if ($LASTEXITCODE -ne 0) { throw 'the agent did not build' }

Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH

Write-Host 'Building the application...'
wails build
if ($LASTEXITCODE -ne 0) { throw 'wails build failed' }

Write-Host 'Done: build\bin\Music Library Organizer.exe'
