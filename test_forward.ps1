# 转发链路测试（直连模式 + 回显服务器）
$ErrorActionPreference = 'Continue'

# ---- TCP 测试：SOCKS5 握手应被转发到回显服务器 ----
try {
  $c = New-Object System.Net.Sockets.TcpClient('127.0.0.1', 1500)
  $s = $c.GetStream()
  $s.WriteTimeout = 3000
  $s.ReadTimeout = 5000
  $payload = [byte[]](0x05, 0x01, 0x00)
  $s.Write($payload, 0, 3)
  Start-Sleep -Milliseconds 500
  $buf = New-Object byte[] 64
  $n = $s.Read($buf, 0, 64)
  $resp = [System.Text.Encoding]::ASCII.GetString($buf, 0, $n)
  Write-Output ("TCP转发(SOCKS5握手): 收到回显 '" + $resp + "'")
  if ($resp.StartsWith("ECHO(3)")) { Write-Output "  -> TCP 转发链路 OK" } else { Write-Output "  -> 转发异常!" }
  $c.Close()
} catch {
  Write-Output ("TCP转发失败: " + $_.Exception.Message)
}

# ---- UDP 测试：WireGuard 风格数据报应被转发 ----
try {
  $u = New-Object System.Net.Sockets.UdpClient
  $u.Client.ReceiveTimeout = 5000
  $wg = New-Object byte[] 148
  $wg[0] = 1; $wg[1] = 0; $wg[2] = 0; $wg[3] = 0
  $remote = New-Object System.Net.IPEndPoint([System.Net.IPAddress]::Parse('127.0.0.1'), 1500)
  $u.Send($wg, 148, $remote) | Out-Null
  $ep = New-Object System.Net.IPEndPoint([System.Net.IPAddress]::Any, 0)
  $resp = $u.Receive([ref]$ep)
  $len = $resp.Length
  Write-Output ("UDP转发(WireGuard握手): 收到回显 " + $len + " 字节")
  if ($len -eq 148 + 5) { Write-Output "  -> UDP 转发链路 OK" } else { Write-Output "  -> 转发异常!" }
  $u.Close()
} catch {
  Write-Output ("UDP转发失败: " + $_.Exception.Message)
}
