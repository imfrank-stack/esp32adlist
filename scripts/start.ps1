<#
.SYNOPSIS
    ESP32 广告过滤规则 API —— Windows 一键构建 + 启动脚本

.DESCRIPTION
    1. 检测 Go 环境，缺失时自动下载便携版到 .tools\go（不写注册表、不需要管理员）
    2. 编译 bin\builder.exe 与 bin\server.exe
    3. 规则数据：默认读 data\raw 里的本地副本离线构建；缺哪个源才补拉哪个
       （-Refresh 从本地重建 data\dist，-Update 联网更新全部规则源）
    4. 启动服务（默认前台，-Background 后台）

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File scripts\start.ps1
    powershell -ExecutionPolicy Bypass -File scripts\start.ps1 -Port 9090 -Background
    powershell -ExecutionPolicy Bypass -File scripts\start.ps1 -Refresh
    powershell -ExecutionPolicy Bypass -File scripts\start.ps1 -Update
    powershell -ExecutionPolicy Bypass -File scripts\start.ps1 -Stop
#>
[CmdletBinding()]
param(
    [int]$Port = 8080,
    [switch]$Refresh,
    [switch]$Update,
    [switch]$Background,
    [switch]$Stop,
    [switch]$InstallGo,
    [string]$GoVersion = ""
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

# Go/curl 等原生程序输出 UTF-8，控制台默认按 GBK 解码会变乱码
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch { }

$Root = Split-Path -Parent $PSScriptRoot
$ToolsDir = Join-Path $Root ".tools"
$LocalGo = Join-Path $ToolsDir "go"
$BinDir = Join-Path $Root "bin"
$LogDir = Join-Path $Root "logs"
$RunDir = Join-Path $Root "run"
$PidFile = Join-Path $RunDir "server.pid"
$DistManifest = Join-Path $Root "data\dist\manifest.json"

function Write-Step($msg) { Write-Host "`n==> $msg" -ForegroundColor Cyan }
function Write-Ok($msg) { Write-Host "    $msg" -ForegroundColor Green }
function Write-Warn2($msg) { Write-Host "    $msg" -ForegroundColor Yellow }
function Fail($msg) {
    Write-Host "`n[错误] $msg" -ForegroundColor Red
    exit 1
}

# ---------- 停止 ----------
if ($Stop) {
    if (Test-Path $PidFile) {
        $procId = (Get-Content $PidFile | Select-Object -First 1).Trim()
        $p = Get-Process -Id $procId -ErrorAction SilentlyContinue
        if ($p) {
            Stop-Process -Id $procId -Force
            Write-Ok "已停止 server.exe (PID $procId)"
        } else {
            Write-Warn2 "PID $procId 已不存在"
        }
        Remove-Item $PidFile -Force -ErrorAction SilentlyContinue
    } else {
        Write-Warn2 "没有找到 run\server.pid，尝试按进程名结束"
        Get-Process -Name server -ErrorAction SilentlyContinue | Stop-Process -Force
    }
    exit 0
}

Write-Host "ESP32 规则 API 一键启动" -ForegroundColor White
Write-Host "项目目录: $Root"

# ---------- 下载工具 ----------
# 外部命令的退出码放在脚本变量里，避免把 stdout 和返回值混在同一个管道中。
$script:NativeExit = 0

function Invoke-External {
    param([string]$Exe, [string[]]$Arguments = @(), [switch]$Quiet)
    $old = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        if ($Quiet) {
            & $Exe @Arguments 2>$null | Out-Null
        } else {
            & $Exe @Arguments
        }
    } finally {
        $script:NativeExit = $LASTEXITCODE
        $ErrorActionPreference = $old
    }
}

function Get-Remote($url, $dest) {
    $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
    if ($curl) {
        Invoke-External $curl.Source @("-fsSL", "--retry", "2", "--connect-timeout", "20", "-o", $dest, $url) -Quiet
        return ($script:NativeExit -eq 0 -and (Test-Path $dest))
    }
    try {
        Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing -TimeoutSec 600
        return $true
    } catch {
        return $false
    }
}

function Expand-Package($archive, $dest) {
    $tar = Get-Command tar.exe -ErrorAction SilentlyContinue
    if ($tar) {
        Invoke-External $tar.Source @("-xf", $archive, "-C", $dest) -Quiet
        if ($script:NativeExit -eq 0) { return $true }
    }
    try {
        Expand-Archive -Path $archive -DestinationPath $dest -Force
        return $true
    } catch {
        return $false
    }
}

# ---------- Go 环境 ----------
function Get-GoMinor($goExe) {
    $old = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $out = (& $goExe version 2>$null) -join " "
    } finally {
        $ErrorActionPreference = $old
    }
    if ($out -match "go(\d+)\.(\d+)") {
        return [int]$Matches[1] * 100 + [int]$Matches[2]
    }
    return 0
}

function Find-Go {
    $candidates = @()
    $candidates += (Join-Path $LocalGo "bin\go.exe")
    $cmd = Get-Command go -ErrorAction SilentlyContinue
    if ($cmd) { $candidates += $cmd.Source }
    $candidates += (Join-Path $env:ProgramFiles "Go\bin\go.exe")
    $candidates += "C:\Go\bin\go.exe"

    foreach ($c in $candidates) {
        if ($c -and (Test-Path $c)) {
            if ((Get-GoMinor $c) -ge 124) { return $c }
        }
    }
    return $null
}

function Install-Go {
    $ver = $GoVersion
    if (-not $ver) { $ver = "1.24.0" }
    $ver = $ver -replace '^(go|v)', ''

    $arch = "amd64"
    $cpu = $env:PROCESSOR_ARCHITECTURE
    if ($env:PROCESSOR_ARCHITEW6432) { $cpu = $env:PROCESSOR_ARCHITEW6432 }
    if ($cpu -eq "ARM64") { $arch = "arm64" }
    elseif ($cpu -eq "x86") { $arch = "386" }

    $file = "go$ver.windows-$arch.zip"
    $mirrors = @(
        "https://mirrors.aliyun.com/golang/$file",
        "https://mirrors.ustc.edu.cn/golang/$file",
        "https://golang.google.cn/dl/$file",
        "https://go.dev/dl/$file"
    )

    if (-not (Test-Path $ToolsDir)) { New-Item -ItemType Directory -Path $ToolsDir | Out-Null }
    $archive = Join-Path $ToolsDir $file

    foreach ($url in $mirrors) {
        Write-Host "    尝试 $url" -ForegroundColor DarkGray
        if (Get-Remote $url $archive) {
            $size = [math]::Round((Get-Item $archive).Length / 1MB, 1)
            Write-Ok "已下载 $file ($size MB)"
            break
        }
    }
    if (-not (Test-Path $archive)) {
        Fail "Go 下载失败。可手动下载 $file 放到 .tools\ 后重试，或先自行安装 Go 1.24+ 加入 PATH。"
    }

    if (Test-Path $LocalGo) { Remove-Item $LocalGo -Recurse -Force }
    Write-Host "    解压中..." -ForegroundColor DarkGray
    $expanded = Expand-Package $archive $ToolsDir
    if (-not $expanded) {
        Fail "解压失败：$archive"
    }
    Remove-Item $archive -Force -ErrorAction SilentlyContinue

    $goExe = Join-Path $LocalGo "bin\go.exe"
    if (-not (Test-Path $goExe)) { Fail "解压后未找到 go.exe" }
    Write-Ok "便携 Go 已就绪: $goExe"
    return $goExe
}

Write-Step "检查 Go 环境"
$goExe = $null
if (-not $InstallGo) { $goExe = Find-Go }

if ($goExe) {
    $verLine = (& $goExe version) -join " "
    Write-Ok "使用已有 Go：$verLine"
} else {
    Write-Warn2 "未找到 Go 1.24+，开始下载便携版（约 80MB）"
    $goExe = Install-Go
    if (-not $goExe) { Fail "Go 环境准备失败" }
}

$env:GOTOOLCHAIN = "local"
$env:GOPROXY = "https://goproxy.cn,direct"
$env:CGO_ENABLED = "0"

# ---------- 编译 ----------
Write-Step "编译"
if (-not (Test-Path $BinDir)) { New-Item -ItemType Directory -Path $BinDir | Out-Null }
Push-Location $Root
try {
    Invoke-External $goExe @("build", "-trimpath", "-o", (Join-Path $BinDir "builder.exe"), "./cmd/builder")
    if ($script:NativeExit -ne 0) { Fail "builder 编译失败" }
    Invoke-External $goExe @("build", "-trimpath", "-o", (Join-Path $BinDir "server.exe"), "./cmd/server")
    if ($script:NativeExit -ne 0) { Fail "server 编译失败" }
} finally {
    Pop-Location
}
Write-Ok "bin\builder.exe / bin\server.exe"

# ---------- 规则数据 ----------
Write-Step "检查规则数据"

# 先盘点本地规则源：data\raw 里的副本是随仓库保存的，默认只读这些，不联网。
$rawMissing = @()
$rawTotal = 0
try {
    $srcCfg = Get-Content (Join-Path $Root "config\sources.json") -Raw -Encoding UTF8 | ConvertFrom-Json
    foreach ($s in $srcCfg.sources) {
        $rel = $s.files[0]
        if (-not $rel) { continue }
        $rawTotal++
        $full = Join-Path $Root $rel
        $exists = $false
        if (Test-Path $full) {
            $len = (Get-Item $full).Length
            if ($len -ge 1024) { $exists = $true }
        }
        if (-not $exists) { $rawMissing += $s.name }
    }
} catch {
    Write-Warn2 "读取 config\sources.json 失败，交给构建器自行判断"
}

if ($rawTotal -gt 0) {
    if ($rawMissing.Count -eq 0) {
        Write-Ok ("本地规则源完整：{0}/{0} 个，走离线构建" -f $rawTotal)
    } else {
        Write-Warn2 ("本地规则源缺 {0}/{1} 个：{2}" -f $rawMissing.Count, $rawTotal, ($rawMissing -join ", "))
        Write-Warn2 "将对这些源联网补拉，其余沿用本地副本"
    }
}

$mode = "auto"
if ($Update) { $mode = "always" }

$needBuild = $false
if ($Update) {
    Write-Ok "按 -Update 重新下载全部规则源"
    $needBuild = $true
} elseif ($Refresh) {
    Write-Ok "按 -Refresh 从本地规则源重建 data\dist"
    $needBuild = $true
} elseif (-not (Test-Path $DistManifest)) {
    Write-Ok "尚无 data\dist\manifest.json，构建一次"
    $needBuild = $true
} elseif ($rawMissing.Count -gt 0) {
    Write-Ok "补全缺失规则源后重建"
    $needBuild = $true
}

if ($needBuild) {
    Push-Location $Root
    try {
        Invoke-External (Join-Path $BinDir "builder.exe") @("-prepare", $mode, "-config", "config/sources.json", "-out", "data/dist", "-basedir", ".")
        if ($script:NativeExit -ne 0) { Fail "规则构建失败（检查 config\sources.json 或网络）" }
    } finally {
        Pop-Location
    }
} else {
    $raw = Get-Content $DistManifest -Raw -Encoding UTF8
    $ver = "unknown"
    if ($raw -match '"version"\s*:\s*"([^"]+)"') { $ver = $Matches[1] }
    $total = 0
    if ($raw -match '"total_unique_domains"\s*:\s*(\d+)') { $total = [int]$Matches[1] }
    Write-Ok ("沿用现有 data\dist，版本 {0}，{1:N0} 个唯一域名（-Refresh 重建 / -Update 联网更新源）" -f $ver, $total)
}

# ---------- 启动 ----------
Write-Step "启动服务"
$baseUrl = "http://127.0.0.1:$Port"

function Test-Health($url) {
    try {
        $r = Invoke-RestMethod -Uri "$url/healthz" -TimeoutSec 2
        return $r
    } catch {
        return $null
    }
}

$alive = Test-Health $baseUrl
if ($alive) {
    Write-Ok "服务已在运行：$baseUrl  (版本 $($alive.version))"
    Write-Host "    文档页: $baseUrl/" -ForegroundColor White
    exit 0
}

if (-not (Test-Path $LogDir)) { New-Item -ItemType Directory -Path $LogDir | Out-Null }
if (-not (Test-Path $RunDir)) { New-Item -ItemType Directory -Path $RunDir | Out-Null }

$serverExe = Join-Path $BinDir "server.exe"

if ($Background) {
    $outLog = Join-Path $LogDir "server.log"
    $distDir = Join-Path $Root "data\dist"

    # 用 WMI 启动，进程完全脱离当前控制台，关掉窗口也不会被带走；
    # 日志由服务端 -log 落盘，避免 stdout 重定向缓冲区塞满导致卡死。
    $cmdLine = '"{0}" -addr ":{1}" -dir "{2}" -log "{3}"' -f $serverExe, $Port, $distDir, $outLog
    $startedId = 0
    try {
        $r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = $cmdLine } -ErrorAction Stop
        if ($r.ReturnValue -eq 0) { $startedId = [int]$r.ProcessId }
    } catch {
        $startedId = 0
    }
    if ($startedId -eq 0) {
        # 退路：没有 WMI 时用 .NET 启动（进程随控制台退出）
        $psi = New-Object System.Diagnostics.ProcessStartInfo
        $psi.FileName = $serverExe
        $psi.Arguments = '-addr ":' + $Port + '" -dir "' + $distDir + '" -log "' + $outLog + '"'
        $psi.WorkingDirectory = $Root
        $psi.UseShellExecute = $false
        $psi.CreateNoWindow = $true
        $proc = [System.Diagnostics.Process]::Start($psi)
        $startedId = $proc.Id
    }
    Set-Content -Path $PidFile -Value $startedId

    $ok = $false
    for ($i = 0; $i -lt 20; $i++) {
        Start-Sleep -Milliseconds 400
        if (Test-Health $baseUrl) { $ok = $true; break }
        if (-not (Get-Process -Id $startedId -ErrorAction SilentlyContinue)) { break }
    }
    if ($ok) {
        Write-Ok "后台启动成功，PID $startedId"
        Write-Host "    文档页: $baseUrl/" -ForegroundColor White
        Write-Host "    日志:   $outLog" -ForegroundColor DarkGray
        Write-Host "    停止:   scripts\start.ps1 -Stop" -ForegroundColor DarkGray
    } else {
        Write-Warn2 "启动失败，日志尾部："
        if (Test-Path $outLog) { Get-Content $outLog -Tail 15 -Encoding UTF8 | ForEach-Object { Write-Host "    $_" } }
        Remove-Item $PidFile -Force -ErrorAction SilentlyContinue
        exit 1
    }
} else {
    Write-Host "    前台运行，Ctrl+C 结束" -ForegroundColor DarkGray
    if ($Port -ne 8080) {
        Write-Host "    注意：绑定 0.0.0.0:$Port，首次运行可能弹出 Windows 防火墙提示" -ForegroundColor DarkGray
    }
    Push-Location $Root
    $ErrorActionPreference = "Continue"
    try {
        & $serverExe -addr ":$Port" -dir "data/dist"
    } finally {
        Pop-Location
    }
}
