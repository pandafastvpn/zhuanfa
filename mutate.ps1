# 修改测试数据：将组3配额设为1GB且已用1GB（超限）
Get-Process zhuanfa-panel -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 500
$path = 'C:\Users\Administrator\Desktop\1\zhuanfa1\testdata\db.json'
$enc = New-Object System.Text.UTF8Encoding $false
$j = [System.IO.File]::ReadAllText($path, $enc) | ConvertFrom-Json
foreach ($g in $j.groups) {
  if ($g.id -eq 3) { $g.quota_gb = 1; $g.total_bytes = 1073741824 }
}
$out = $j | ConvertTo-Json -Depth 10
[System.IO.File]::WriteAllText($path, $out, $enc)
Write-Output 'DB_MUTATED_OK'
