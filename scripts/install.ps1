$ErrorActionPreference = "Stop"

$Repo = "voocel/codebot"
$Binary = "codebot"

# Detect architecture.
$Arch = if ([Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "x86_64" }
} else {
    Write-Error "Unsupported: 32-bit OS"; exit 1
}

# Determine version.
$Version = if ($args.Count -gt 0) { $args[0] }
           elseif ($env:CODEBOT_VERSION) { $env:CODEBOT_VERSION }
           else {
               $release = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
               $release.tag_name
           }

if (-not $Version) { Write-Error "Failed to fetch latest version"; exit 1 }

$VersionNum = $Version.TrimStart("v")
$Filename = "${Binary}_${VersionNum}_Windows_${Arch}.zip"
$Url = "https://github.com/$Repo/releases/download/$Version/$Filename"

Write-Host "Downloading $Binary $Version (Windows/$Arch)..."
$TmpDir = Join-Path $env:TEMP "codebot-install"
New-Item -ItemType Directory -Force -Path $TmpDir | Out-Null

try {
    Invoke-WebRequest -Uri $Url -OutFile "$TmpDir\$Filename"
    Expand-Archive -Path "$TmpDir\$Filename" -DestinationPath $TmpDir -Force

    # Install to user-local bin directory.
    $InstallDir = Join-Path $env:LOCALAPPDATA "codebot"
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Move-Item -Force "$TmpDir\$Binary.exe" "$InstallDir\$Binary.exe"

    # Add to PATH if not already present.
    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($UserPath -notlike "*$InstallDir*") {
        [Environment]::SetEnvironmentVariable("Path", "$UserPath;$InstallDir", "User")
        Write-Host "Added $InstallDir to user PATH (restart terminal to take effect)"
    }

    # Create global config directory and default AGENTS.md if not present.
    $ConfigDir = Join-Path $env:USERPROFILE ".codebot"
    $AgentsFile = Join-Path $ConfigDir "AGENTS.md"
    New-Item -ItemType Directory -Force -Path $ConfigDir | Out-Null
    if (-not (Test-Path $AgentsFile)) {
        @"
# Codebot

You are Codebot, an AI coding assistant that runs in the terminal.
You help developers read, write, and refactor code through direct filesystem and shell access.

# This file is loaded for every project as the lowest-priority context.
# Add your personal preferences and conventions here.
# Project-level AGENTS.md (in the project root) takes higher priority.

## Code Style
- Prefer simple, correct solutions over clever ones
- Follow existing conventions in each project

## Communication
- Be concise and direct
- Explain the "why" before making changes
"@ | Set-Content -Path $AgentsFile -Encoding UTF8
        Write-Host "Created default $AgentsFile"
    }

    Write-Host "$Binary $Version installed to $InstallDir\$Binary.exe"
} finally {
    Remove-Item -Recurse -Force $TmpDir -ErrorAction SilentlyContinue
}
