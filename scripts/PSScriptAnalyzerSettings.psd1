# Settings for Invoke-ScriptAnalyzer over the PowerShell scripts in this directory.
# Run: pwsh scripts/ci-local.ps1 -Lint   (or Invoke-ScriptAnalyzer -Path scripts -Recurse -Settings scripts/PSScriptAnalyzerSettings.psd1)
# Findings of severity Warning or Error fail the run. Fix findings; do not suppress them.
@{
  IncludeDefaultRules = $true
  Severity            = @('Error', 'Warning')
}

