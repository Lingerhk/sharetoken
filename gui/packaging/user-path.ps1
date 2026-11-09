# Adds this directory to, or removes it from, the current user's PATH, for the
# Windows installer. The registry is used directly because the .NET environment
# call would expand and rewrite the user's other entries.
param([Parameter(Mandatory)][ValidateSet('add','remove')][string]$Action, [Parameter(Mandatory)][string]$Dir)
$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
$old = $key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
$parts = @($old -split ';' | Where-Object { $_ -and $_.TrimEnd('\') -ine $Dir.TrimEnd('\') })
if ($Action -eq 'add') { $parts += $Dir }
$key.SetValue('Path', ($parts -join ';'), [Microsoft.Win32.RegistryValueKind]::ExpandString)
