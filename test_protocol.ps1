# 协议检测冒烟测试
param(
  [int]$Port = 1500
)

function Test-Protocol([string]$name, [byte[]]$payload, [int]$readBytes = 16) {
  try {
    $c = New-Object System.Net.Sockets.TcpClient('127.0.0.1', $Port)
    $s = $c.GetStream()
    $s.WriteTimeout = 3000
    $s.ReadTimeout = 3000
    $s.Write($payload, 0, $payload.Length)
    Start-Sleep -Milliseconds 400
    $buf = New-Object byte[] $readBytes
    try {
      $n = $s.Read($buf, 0, $readBytes)
      Write-Output ("{0}: server reply {1} bytes" -f $name, $n)
    } catch {
      Write-Output ("{0}: server closed/read error ({1}) - 预期行为" -f $name, $_.Exception.GetType().Name)
    }
    $c.Close()
  } catch {
    Write-Output ("{0}: connect error: {1}" -f $name, $_.Exception.Message)
  }
}

# 1. SOCKS5 握手: 05 01 00 (VER=5, 1 method, no-auth)
Test-Protocol "SOCKS5 " @(0x05, 0x01, 0x00)

# 2. WireGuard 握手发起 (type=1, 148 bytes)
$wg = New-Object byte[] 148
$wg[0] = 1; $wg[1] = 0; $wg[2] = 0; $wg[3] = 0
Test-Protocol "WireGuard" $wg

# 3. OpenVPN 硬重置 V2 (opcode=7<<3=0x38), 30 bytes
$ov = New-Object byte[] 30
$ov[0] = 0x38
Test-Protocol "OpenVPN " $ov

# 4. HTTP 请求 (应被拒绝)
$http = [System.Text.Encoding]::ASCII.GetBytes("GET / HTTP/1.1`r`nHost: x`r`n`r`n")
Test-Protocol "HTTP拒绝" $http

# 5. SSH 特征 (应被拒绝)
$ssh = [System.Text.Encoding]::ASCII.GetBytes("SSH-2.0-OpenSSH_9.6")
Test-Protocol "SSH拒绝 " $ssh

# 6. 空连接（不发数据，应超时拒绝）
try {
  $c = New-Object System.Net.Sockets.TcpClient('127.0.0.1', $Port)
  $s = $c.GetStream()
  $s.ReadTimeout = 6000
  $buf = New-Object byte[] 16
  try {
    $n = $s.Read($buf, 0, 16)
    Write-Output ("空连接: server reply {0} bytes" -f $n)
  } catch {
    Write-Output ("空连接: server closed/read error ({0}) - 预期行为" -f $_.Exception.GetType().Name)
  }
  $c.Close()
} catch {
  Write-Output ("空连接: connect error: {0}" -f $_.Exception.Message)
}
