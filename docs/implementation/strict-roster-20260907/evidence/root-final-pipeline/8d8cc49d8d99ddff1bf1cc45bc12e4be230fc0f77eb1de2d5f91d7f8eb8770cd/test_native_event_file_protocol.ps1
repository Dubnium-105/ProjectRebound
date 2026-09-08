$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$taskSource = 'C:/wksp/ProjectRebound/.tmp/strict-roster-20260907/native-evidence/orchestrate-dedicated-one-real-steam-positive.ps1'
$taskRoot = 'C:/wksp/ProjectRebound/.tmp/strict-roster-20260907/native-evidence/event-file-tests-' + [DateTimeOffset]::UtcNow.ToString('yyyyMMddTHHmmssfffffffZ')
New-Item -ItemType Directory -Path $taskRoot -ErrorAction Stop | Out-Null
Copy-Item -LiteralPath $taskSource -Destination (Join-Path $taskRoot 'input-harness.ps1')
$taskTokens = $null
$taskErrors = $null
$taskAst = [Management.Automation.Language.Parser]::ParseFile($taskSource, [ref]$taskTokens, [ref]$taskErrors)
if ($taskErrors.Count) { throw 'Harness syntax failed' }
foreach ($taskFunctionName in @('Get-PrivateScopedEvents', 'Write-PrivateScopedEnvelope')) {
    $taskFunction = $taskAst.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $taskFunctionName}, $true)
    if ($null -eq $taskFunction) { throw "Missing function $taskFunctionName" }
    Invoke-Expression $taskFunction.Extent.Text
}
$taskPath = Join-Path $taskRoot 'unit-events.json'
Write-PrivateScopedEnvelope -Path $taskPath -Events @() -NextSequence 0
if (@(Get-PrivateScopedEvents -Path $taskPath).Count -ne 0) { throw 'Empty event envelope did not round trip' }
for ($taskIndex = 1; $taskIndex -le 100; ++$taskIndex) {
    Write-PrivateScopedEnvelope -Path $taskPath -Events @([pscustomobject]@{unit_test_sequence=$taskIndex}) -NextSequence $taskIndex
    $taskReadBack = @(Get-PrivateScopedEvents -Path $taskPath)
    if ($taskReadBack.Count -ne 1 -or $taskReadBack[0].unit_test_sequence -ne $taskIndex) { throw 'Atomic replacement did not retain the complete envelope' }
}
[IO.File]::WriteAllText($taskPath, '{"events":null,"next_sequence":100}')
$taskRejected = $false
try { [void](Get-PrivateScopedEvents -Path $taskPath) } catch { $taskRejected = $true }
if (-not $taskRejected) { throw 'Malformed envelope was silently reset' }
if ([IO.File]::ReadAllText($taskPath) -ne '{"events":null,"next_sequence":100}') { throw 'Rejected envelope was overwritten' }
$taskReceipt = [ordered]@{
    status='PASS_FILE_COMPONENT_ONLY'; exit_code=0; tests_passed=3
    tests=@('empty envelope round trip','100 existing-file atomic replacements','malformed envelope rejected without resetting cursor')
    native_admission_executed=$false
    input_harness_sha256=(Get-FileHash -Algorithm SHA256 (Join-Path $taskRoot 'input-harness.ps1')).Hash
    observed_at=[DateTimeOffset]::UtcNow.ToString('o')
}
$taskReceipt | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $taskRoot 'receipt.json') -Encoding utf8
$taskReceipt | ConvertTo-Json -Compress -Depth 5
Write-Output "Receipt: $taskRoot/receipt.json"
