# Remap stale commit SHAs in docs/ using .git/filter-repo/commit-map.
#
# Background: git filter-repo ran on 2026-11-13 01:13 (scrubbing internal IP and
# embedded token). main / gitea/main / v0.1.0..v0.1.2 all got new hashes, 135 of
# 275 commits were rewritten. Every commit SHA cited in the docs is now dangling.
#
# Usage:
#   .\scripts\fix_doc_shas.ps1           # dry-run, report only
#   .\scripts\fix_doc_shas.ps1 -Apply    # actually write
#
# Conservative rules:
#   1. Only docs/**/*.md, skipping "90 - " (archive is read-only history).
#   2. Only backtick-wrapped 7..40 hex chars (matches existing doc style).
#   3. Only rewrite when the SHA does not exist locally AND maps uniquely by its
#      7-char prefix.
#   4. The mapped target must exist locally, else skip and report.
#   5. Same old SHA always maps to the same new SHA, so re-running is idempotent.

param(
    [switch]$Apply
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path $PSScriptRoot -Parent
$mapFile = Join-Path $repo '.git/filter-repo/commit-map'
if (-not (Test-Path $mapFile)) { throw "missing $mapFile" }

$map = @{}
foreach ($line in (Get-Content $mapFile | Select-Object -Skip 1)) {
    if ($line -match '^([0-9a-f]{40}) ([0-9a-f]{40})$') {
        $map[$Matches[1]] = $Matches[2]
    }
}
if ($map.Count -eq 0) { throw 'commit-map is empty' }

# old(7-char prefix) -> new(40)
$byPrefix = @{}
foreach ($k in $map.Keys) { $byPrefix[$k.Substring(0, 7)] = $map[$k] }

function Test-ShaExists([string]$sha) {
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $null = git -C $repo cat-file -e $sha 2>&1
    $ok = ($LASTEXITCODE -eq 0)
    $ErrorActionPreference = $prev
    return $ok
}

$stats = @{
    Files          = 0
    Replaced       = 0
    NoMap          = New-Object System.Collections.ArrayList
    BadTarget      = New-Object System.Collections.ArrayList
    Details        = New-Object System.Collections.ArrayList
}

$docs = Get-ChildItem -Path (Join-Path $repo 'docs') -Recurse -Filter '*.md' -File |
    Where-Object { $_.FullName -notmatch '90 - ' }

foreach ($doc in $docs) {
    $text = [System.IO.File]::ReadAllText($doc.FullName, [System.Text.Encoding]::UTF8)
    $original = $text

    $text = [regex]::Replace($text, '`([0-9a-f]{7,40})`', {
        param($m)
        $sha = $m.Groups[1].Value
        if (Test-ShaExists $sha) { return $m.Value }
        $p7 = $sha.Substring(0, 7)
        if (-not $byPrefix.ContainsKey($p7)) {
            [void]$stats.NoMap.Add("$($doc.Name) : $sha")
            return $m.Value
        }
        $new = $byPrefix[$p7]
        if (-not (Test-ShaExists $new)) {
            [void]$stats.BadTarget.Add("$($doc.Name) : $sha -> $($new.Substring(0,7))")
            return $m.Value
        }
        $len = [Math]::Min($sha.Length, $new.Length)
        $short = $new.Substring(0, $len)
        $stats.Replaced++
        [void]$stats.Details.Add("$($doc.Name) : $sha -> $short")
        return ([string][char]96 + $short + [string][char]96)
    })

    if ($text -ne $original) {
        $stats.Files++
        if ($Apply) {
            # UTF-8 without BOM (repo convention for docs).
            [System.IO.File]::WriteAllText($doc.FullName, $text, (New-Object System.Text.UTF8Encoding($false)))
        }
    }
}

Write-Host "=== mode: $(if ($Apply) { 'APPLY (written)' } else { 'DRY-RUN (nothing written)' }) ==="
Write-Host "docs scanned: $($docs.Count) (archive skipped)"
Write-Host "files to change: $($stats.Files)"
Write-Host "shas replaceable: $($stats.Replaced)"
Write-Host "no mapping: $($stats.NoMap.Count)"
foreach ($s in $stats.NoMap) { Write-Host "  - $s" }
if ($stats.BadTarget.Count -gt 0) {
    Write-Host "bad target: $($stats.BadTarget.Count)"
    foreach ($s in $stats.BadTarget) { Write-Host "  - $s" }
}
if ($stats.Replaced -gt 0) {
    Write-Host ""
    Write-Host "=== details (first 25) ==="
    $stats.Details | Select-Object -First 25 | ForEach-Object { Write-Host "  $_" }
}
