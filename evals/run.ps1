
# 评测模式（消融基线对照）：
#   full        完整 TraceMind（默认）
#   no-skills   关闭技能库（对照"无 Skill 多轮 Agent"）
#   single-shot 单轮 LLM 基线（不经调查直接诊断）
param(
    [ValidateSet("full", "no-skills", "single-shot")]
    [string]$Mode = "full"
)

# 不能设为 Stop：go run 的 stderr（如 Partial report、警告）经 2>&1 捕获后会变成
# 终止性错误记录，导致脚本中断且不写日志。Continue 下脚本会继续跑完所有 case，
# 文件缺失等真实问题仍由显式 throw 处理。
$ErrorActionPreference = "Continue"

# Always run from the repository root.
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

$observationsPath = Join-Path $repoRoot "evals/datasets/observations.json"
$casesPath = Join-Path $repoRoot "evals/datasets/cases.json"
$resultsDir = Join-Path $repoRoot "evals/results"
if ($Mode -ne "full") {
    $resultsDir = Join-Path $resultsDir $Mode
}

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
    # 先清零上次的退出码，避免 go 启动失败时误用陈旧值。
    $LASTEXITCODE = 0
    $extraArgs = @()
    if ($Mode -eq "no-skills") { $extraArgs += "-no-skills" }
    if ($Mode -eq "single-shot") { $extraArgs += "-single-shot" }
    $output = @(
        & go run ./cmd/agent `
            -config config.yaml `
            -incident $case.incident `
            -dataset $observationsPath `
            -scenario $case.id `
            @extraArgs 2>&1 |
            ForEach-Object { "$_" }
    )
    $exitCode = $LASTEXITCODE
    $rawOutput = $output -join [Environment]::NewLine

    $rawPath = Join-Path $resultsDir "$($case.id).log"
    Set-Content -Path $rawPath -Value $rawOutput -Encoding UTF8

    if ($exitCode -ne 0) {
        Write-Warning "Scenario failed to execute. Exit code: $exitCode"

        $results.Add([PSCustomObject]@{
            scenario        = $case.id
            score           = 0
            score_task      = 0
            score_root_cause = 0
            score_evidence  = 0
            score_efficiency = 0
            expected_hit    = $false
            forbidden_hits  = "execution_failed"
            tool_calls      = -1
            tool_calls_max  = [int]$case.expected_tool_calls_max
            state           = "execution_failed"
            error           = "Process exit code $exitCode"
        })
        continue
    }

    # The agent prints tool traces before the JSON report.
    # Find the first line beginning with an opening brace.
    $jsonStart = [regex]::Match($rawOutput, '(?m)^\{')

    if (-not $jsonStart.Success) {
        Write-Warning "No JSON report found. See $rawPath"

        $results.Add([PSCustomObject]@{
            scenario        = $case.id
            score           = 0
            score_task      = 0
            score_root_cause = 0
            score_evidence  = 0
            score_efficiency = 0
            expected_hit    = $false
            forbidden_hits  = "missing_json_report"
            tool_calls      = -1
            tool_calls_max  = [int]$case.expected_tool_calls_max
            state           = "invalid_output"
            error           = "Could not locate JSON report"
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
            scenario        = $case.id
            score           = 0
            score_task      = 0
            score_root_cause = 0
            score_evidence  = 0
            score_efficiency = 0
            expected_hit    = $false
            forbidden_hits  = "invalid_json"
            tool_calls      = -1
            tool_calls_max  = [int]$case.expected_tool_calls_max
            state           = "invalid_output"
            error           = $_.Exception.Message
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

    $toolCalls = [int]$report.tool_calls
    $maxCalls = [int]$case.expected_tool_calls_max

    # ---------- 四维评分（合计 100）----------
    # 1) 任务完成 (20)：正常结束并产出报告。
    $scoreTask = 0
    if ([string]$report.state -eq "completed") { $scoreTask = 20 }

    # 2) 根因质量 (40)：命中任一期望关键词 +40；命中违禁词则该维度归零。
    $scoreRootCause = 0
    if ($expectedHit) { $scoreRootCause = 40 }
    if ($forbiddenHits.Count -gt 0) { $scoreRootCause = 0 }

    # 3) 证据质量 (20)：结构化假设引用的证据 ID 必须真实存在于报告证据列表。
    $scoreEvidence = 0
    $evidenceIDs = @{}
    foreach ($ev in @($report.evidence)) {
        if ($ev.id) { $evidenceIDs[[string]$ev.id] = $true }
    }
    $hypothesisRefs = @()
    foreach ($h in @($report.diagnosis_hypotheses)) {
        foreach ($link in @($h.evidence)) {
            if ($link.evidence_id) { $hypothesisRefs += [string]$link.evidence_id }
        }
    }
    if ($hypothesisRefs.Count -eq 0) {
        # 未输出结构化假设：给中性分，不因模型风格差异过度惩罚。
        $scoreEvidence = 10
    }
    else {
        $invalidRefs = @(
            $hypothesisRefs | Where-Object { -not $evidenceIDs.ContainsKey($_) }
        )
        if ($invalidRefs.Count -eq 0) {
            $scoreEvidence = 20
        }
        else {
            $validRatio = ($hypothesisRefs.Count - $invalidRefs.Count) / $hypothesisRefs.Count
            $scoreEvidence = [Math]::Round(20 * $validRatio)
        }
    }

    # 4) 工程效率 (20)：工具调用数在预算内 +20，每超 1 扣 5，最低 0。
    $scoreEfficiency = 20
    if ($toolCalls -gt $maxCalls) {
        $scoreEfficiency = [Math]::Max(0, 20 - 5 * ($toolCalls - $maxCalls))
    }

    $score = $scoreTask + $scoreRootCause + $scoreEvidence + $scoreEfficiency
    $score = [Math]::Min(100, $score)

    $forbiddenText = "none"
    if ($forbiddenHits.Count -gt 0) {
        $forbiddenText = $forbiddenHits -join "; "
    }

    $results.Add([PSCustomObject]@{
        scenario        = $case.id
        score           = $score
        score_task      = $scoreTask
        score_root_cause = $scoreRootCause
        score_evidence  = $scoreEvidence
        score_efficiency = $scoreEfficiency
        expected_hit    = $expectedHit
        matched_terms   = ($matchedExpected -join "; ")
        forbidden_hits  = $forbiddenText
        tool_calls      = $toolCalls
        tool_calls_max  = $maxCalls
        state           = [string]$report.state
        error           = ""
    })

    Write-Host "State: $($report.state)"
    Write-Host "Tool calls: $toolCalls / $maxCalls"
    Write-Host "Expected keyword hit: $expectedHit"
    Write-Host "Forbidden matches: $forbiddenText"
    Write-Host "Score: $score / 100 (task $scoreTask + root-cause $scoreRootCause + evidence $scoreEvidence + efficiency $scoreEfficiency)"
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
