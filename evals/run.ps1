
$ErrorActionPreference = "Stop"

# Always run from the repository root.
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$observationsPath = Join-Path $repoRoot "evals/datasets/observations.json"
$casesPath = Join-Path $repoRoot "evals/datasets/cases.json"
$resultsDir = Join-Path $repoRoot "evals/results"

if (-not (Test-Path $observationsPath)) {
    throw "Observations dataset not found: $observationsPath"
}
if (-not (Test-Path $casesPath)) {
    throw "Evaluation cases not found: $casesPath"
}
if (-not (Test-Path "config.yaml")) {
    throw "config.yaml not found in repository root."
}

New-Item -ItemType Directory -Force -Path $resultsDir | Out-Null

$caseData = Get-Content -Raw -Encoding UTF8 $casesPath | ConvertFrom-Json
$results = [System.Collections.Generic.List[object]]::new()

foreach ($case in $caseData.cases) {
    Write-Host ""
    Write-Host "=================================================="
    Write-Host "Scenario: $($case.id)"
    Write-Host "Incident: $($case.incident)"
    Write-Host "=================================================="

    # Run the existing TraceMind command with this scenario.
    $output = @(
        & go run ./cmd/agent `
            -config config.yaml `
            -incident $case.incident `
            -dataset $observationsPath `
            -scenario $case.id 2>&1 |
            ForEach-Object { "$_" }
    )
    $exitCode = $LASTEXITCODE
    $rawOutput = $output -join [Environment]::NewLine

    $rawPath = Join-Path $resultsDir "$($case.id).log"
    Set-Content -Path $rawPath -Value $rawOutput -Encoding UTF8

    if ($exitCode -ne 0) {
        Write-Warning "Scenario failed to execute. Exit code: $exitCode"

        $results.Add([PSCustomObject]@{
            scenario       = $case.id
            score          = 0
            expected_hit   = $false
            forbidden_hits = "execution_failed"
            tool_calls     = -1
            state          = "execution_failed"
            error          = "Process exit code $exitCode"
        })
        continue
    }

    # The agent prints tool traces before the JSON report.
    # Find the first line beginning with an opening brace.
    $jsonStart = [regex]::Match($rawOutput, '(?m)^\{')

    if (-not $jsonStart.Success) {
        Write-Warning "No JSON report found. See $rawPath"

        $results.Add([PSCustomObject]@{
            scenario       = $case.id
            score          = 0
            expected_hit   = $false
            forbidden_hits = "missing_json_report"
            tool_calls     = -1
            state          = "invalid_output"
            error          = "Could not locate JSON report"
        })
        continue
    }

    $jsonText = $rawOutput.Substring($jsonStart.Index)

    try {
        $report = $jsonText | ConvertFrom-Json
    }
    catch {
        Write-Warning "Could not parse JSON report. See $rawPath"

        $results.Add([PSCustomObject]@{
            scenario       = $case.id
            score          = 0
            expected_hit   = $false
            forbidden_hits = "invalid_json"
            tool_calls     = -1
            state          = "invalid_output"
            error          = $_.Exception.Message
        })
        continue
    }

    # Score the summary and diagnosis hypotheses, rather than raw tool logs.
    $diagnosisText = @(
        [string]$report.summary
        ($report.hypotheses | ForEach-Object { [string]$_ })
        ($report.diagnosis_hypotheses | ForEach-Object {
            [string]$_.title
            [string]$_.description
        })
    ) -join "`n"

    $expectedHit = $false
    $matchedExpected = @()

    foreach ($keyword in $case.expected_keywords) {
        if ($diagnosisText.IndexOf(
            [string]$keyword,
            [StringComparison]::OrdinalIgnoreCase
        ) -ge 0) {
            $expectedHit = $true
            $matchedExpected += [string]$keyword
        }
    }

    $forbiddenHits = @()

    foreach ($keyword in $case.forbidden_keywords) {
        if ($diagnosisText.IndexOf(
            [string]$keyword,
            [StringComparison]::OrdinalIgnoreCase
        ) -ge 0) {
            $forbiddenHits += [string]$keyword
        }
    }

    $score = 0

    if ($expectedHit) {
        $score += 60
    }

    if ($forbiddenHits.Count -eq 0) {
        $score += 40
    }

    $toolCalls = [int]$report.tool_calls
    $maxCalls = [int]$case.expected_tool_calls_max

    if ($toolCalls -gt $maxCalls) {
        $score -= 5 * ($toolCalls - $maxCalls)
    }

    $score = [Math]::Max(0, [Math]::Min(100, $score))

    $forbiddenText = "none"
    if ($forbiddenHits.Count -gt 0) {
        $forbiddenText = $forbiddenHits -join "; "
    }

    $results.Add([PSCustomObject]@{
        scenario       = $case.id
        score          = $score
        expected_hit   = $expectedHit
        matched_terms  = ($matchedExpected -join "; ")
        forbidden_hits = $forbiddenText
        tool_calls     = $toolCalls
        state          = [string]$report.state
        error          = ""
    })

    Write-Host "State: $($report.state)"
    Write-Host "Tool calls: $toolCalls"
    Write-Host "Expected keyword hit: $expectedHit"
    Write-Host "Forbidden matches: $forbiddenText"
    Write-Host "Score: $score / 100"
}

# Save machine-readable results and a CSV for spreadsheet analysis.
$jsonResultPath = Join-Path $resultsDir "summary.json"
$csvResultPath = Join-Path $resultsDir "summary.csv"

$results |
    ConvertTo-Json -Depth 10 |
    Set-Content -Path $jsonResultPath -Encoding UTF8

$results |
    Export-Csv -Path $csvResultPath -NoTypeInformation -Encoding UTF8

Write-Host ""
Write-Host "================ Evaluation Summary ================"

$results |
    Select-Object scenario, score, expected_hit, tool_calls, state |
    Format-Table -AutoSize

$validResults = @(
    $results | Where-Object { $_.state -ne "execution_failed" -and $_.state -ne "invalid_output" }
)

if ($validResults.Count -gt 0) {
    $average = (
        ($validResults | Measure-Object -Property score -Average).Average
    )

    Write-Host ("Average score: {0:N1} / 100" -f $average)
    Write-Host ("Completed evaluations: {0}" -f $validResults.Count)
}

Write-Host ""
Write-Host "Detailed results: $resultsDir"
Write-Host "CSV: $csvResultPath"
Write-Host "JSON: $jsonResultPath"
