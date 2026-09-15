<#
.SYNOPSIS
    收紧 twilight-blog 在 Windows 上承载凭据的数据文件权限。

.DESCRIPTION
    背景（第三轮渗透测试 R3-OPS-2）：

    代码里对敏感文件表达的是「只有属主可读写」这个意图——config.writeFileAtomic
    用 0o600、database.Open 用 0o600/0o700、logging 用 0o600。但 **Go 的 os.Chmod
    在 Windows 上只能切换只读属性，不能限制 ACL**，于是这些意图全部落空：
    实测 settings.json / blog.db / blog.db-wal / log.txt 的有效权限是

        NT AUTHORITY\Authenticated Users   Allow   Modify, Synchronize
        BUILTIN\Users                      Allow   ReadAndExecute, Synchronize

    也就是说本机任意已认证用户都能读写它们。后果是双向的：

      * 读 —— 拿走 settings.json 里迁移前的 bcrypt 哈希、或库里的会话与哈希，
              离线爆破；
      * 写 —— 直接改 blog.db 的 users.password_hash，或者插入一行 is_admin = 1，
              等于交出站点。

    而且这不只是「文件」的问题：父目录的宽松 ACE 会被**新文件继承**，所以
    日志轮转产生的 log.txt.1、上传产生的 media/*.webp、SQLite 重建的 -wal/-shm
    都会重新带上宽松权限。因此本脚本对**目录**设置可继承的 ACE，一次性覆盖
    现在与将来。

    代码侧无法修（Windows 上 os.Chmod 做不到），所以这是一条部署动作。

.PARAMETER AppDir
    twilight-blog 应用目录（含 settings.json / blog.db / media 的那一层）。
    默认取本脚本上一级目录下的 twilight-blog。

.PARAMETER Revert
    把权限恢复成「从父目录继承」。脚本改动的只是显式 ACE 与继承开关，
    /reset 即可回到改动前的状态。

.EXAMPLE
    # 以管理员身份运行
    pwsh -File deploy\harden-windows-acl.ps1 -WhatIf     # 先看会做什么
    pwsh -File deploy\harden-windows-acl.ps1             # 执行

.EXAMPLE
    pwsh -File deploy\harden-windows-acl.ps1 -Revert     # 回滚
#>
[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [string]$AppDir = (Join-Path (Split-Path -Parent $PSScriptRoot) 'twilight-blog'),
    [switch]$Revert
)

$ErrorActionPreference = 'Stop'

function Test-IsElevated {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    (New-Object Security.Principal.WindowsPrincipal $id).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

<#
.SYNOPSIS
    判断一个目标是否**既被收紧、又仍然可用**，返回问题列表（空数组 = 健康）。

.DESCRIPTION
    ⚠️ 这个函数是从一次真实事故里长出来的，不要把它简化回「只看有没有宽松主体」。

    第一版脚本用「没有宽松写入主体」当作成功条件，于是它把两种完全不同的状态
    都判成了 clean：

      * 正确收紧：只剩属主 / SYSTEM / Administrators 的 allow 规则；
      * **被锁死**：DACL 为空（ACE 数 0）——那是对**所有人**拒绝，
        站点连自己的 settings.json / blog.db 都打不开。

    实际发生的就是后者：脚本给**文件**也传了 (OI)(CI)，而那两个是**容器专用**的
    继承标志。用在一个文件上时，icacls 建出的 ACE 不适用于该对象自身，
    结果文件拿到一个空 DACL；下一次启动时 config.Load 直接权限失败、进程退出。
    而自检仍然报 clean——因为它只问「有没有人能写」，没问「属主还能不能用」。

    所以判定必须同时覆盖两面：
      1. ACL 读得到（读不到就是未知，不是健康）；
      2. DACL 非空；
      3. 属主有一条 allow 规则；
      4. 没有宽松写入主体；
      5. **真的打开一次**（文件：以读写方式打开；目录：建一个探针文件再删掉）。

    第 5 条是关键：前面四条都能靠「把权限删光」满足，只有实际访问能证伪它。
#>
function Test-TargetHealth {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$AccountSid,
        [Parameter(Mandatory = $true)][hashtable]$RequiredPrincipals
    )

    $problems = @()

    $acl = $null
    try {
        $acl = Get-Acl -LiteralPath $Path
    } catch {
        # 读不到 ACL 说明当前身份已经失去 READ_CONTROL —— 这是失败，不是「干净」。
        $problems += "无法读取 ACL（$($_.Exception.Message)）"
    }

    if ($acl) {
        if ($acl.Access.Count -eq 0) {
            $problems += 'DACL 为空：对所有人（含属主）拒绝，站点将无法访问该路径'
        }

        # 按 **SID** 比较而不是按名字：同一份授权在中文/英文系统上渲染出来的名字
        # 不一样，按名字比会在别的语言环境下误报。
        $allowSids = @{}
        foreach ($rule in $acl.Access) {
            if ($rule.AccessControlType -ne 'Allow') { continue }
            try { $allowSids[$rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value] = $true } catch { }
        }

        if (-not $allowSids.ContainsKey($AccountSid)) {
            $problems += "运行博客的账号（$AccountSid）没有 allow 规则"
        }

        # 「我下发的三条授权是否真的落地」——icacls 会报告成功却不写进 ACE
        # （受限令牌下加组授权时实测如此），所以必须自己回读确认，不能信它的输出。
        foreach ($label in $RequiredPrincipals.Keys) {
            if (-not $allowSids.ContainsKey($RequiredPrincipals[$label])) {
                $problems += "缺少预期的授权：$label（$($RequiredPrincipals[$label])）—— icacls 可能报告了成功却没有写入"
            }
        }

        $broad = $acl.Access | Where-Object {
            $_.AccessControlType -eq 'Allow' -and
            $_.IdentityReference -match 'Authenticated Users|Everyone|BUILTIN\\Users$' -and
            ($_.FileSystemRights -band [Security.AccessControl.FileSystemRights]::Write) -ne 0
        }
        if ($broad) {
            $problems += '仍可被写入：' + (($broad | ForEach-Object { $_.IdentityReference }) -join ', ')
        }
    }

    # 真实访问测试。目录要建一个临时文件——只检查 ACE 无法发现空 DACL 之类的锁死。
    try {
        if ((Get-Item -LiteralPath $Path).PSIsContainer) {
            $probe = Join-Path $Path ('.acl-probe-{0}.tmp' -f [guid]::NewGuid().ToString('N').Substring(0, 8))
            [IO.File]::WriteAllText($probe, 'probe')
            Remove-Item -LiteralPath $probe -Force
        } else {
            $stream = [IO.File]::Open($Path, 'Open', 'ReadWrite', 'ReadWrite')
            $stream.Close()
        }
    } catch {
        # 说明这条为什么会失败：当前身份若不是运行博客的那个账号，收紧之后本来就
        # 访问不到——那是预期结果，不是脚本出错。不写清楚的话，操作者看到 12 条
        # 红字会以为脚本把事情搞砸了。
        $problems += "实际读写失败（$($_.Exception.Message)）；若当前身份不是运行博客的那个账号，这是预期结果，请换该账号复核"
    }

    return $problems
}

if (-not (Test-Path -LiteralPath $AppDir)) {
    throw "找不到应用目录：$AppDir（用 -AppDir 指定）"
}
$AppDir = (Resolve-Path -LiteralPath $AppDir).Path

if (-not (Test-IsElevated)) {
    Write-Warning '未以管理员身份运行。修改 ACL 需要管理员权限，否则会得到「拒绝访问」。'
    Write-Warning '请用「以管理员身份运行」打开的 PowerShell 重新执行本脚本。'
}

Write-Host "应用目录：$AppDir"

# 目录：需要可继承的 ACE，否则日志轮转、上传、SQLite 重建出来的新文件会重新变宽松。
$directoryTargets = @(
    $AppDir,
    (Join-Path $AppDir 'media'),
    (Join-Path $AppDir 'meilisearch-data'),
    (Join-Path $AppDir 'dumps')
)

# 文件：**只**恢复为继承父目录，绝不自己设 ACE（见 Test-TargetHealth 的说明）。
$fileTargets = @(
    (Join-Path $AppDir 'settings.json'),
    (Join-Path $AppDir 'blog.db'),
    (Join-Path $AppDir 'blog.db-wal'),
    (Join-Path $AppDir 'blog.db-shm'),
    (Join-Path $AppDir 'log.txt')
)

# 核验的目标比「改动目标」更宽：可执行文件虽然不单独改，但必须确认也被收紧了
# （它们继承自应用目录，而应用目录正是本地二进制劫持的入口，见渗透报告 R3-04）。
$verifyTargets = $directoryTargets + $fileTargets + @(
    (Join-Path $AppDir 'main.exe'),
    (Join-Path $AppDir 'bin'),
    (Join-Path $AppDir 'bin\meilisearch-windows-amd64.exe')
)

# 核验用的两个基准在两个分支里都要有：运行博客的账号（按 SID）与期望落地的授权集合。
# -Revert 之后不该再要求那三条授权，所以那里给空表。
$accountSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$required = @{}

if ($Revert) {
    Write-Host "`n恢复为「继承父目录权限」："
    foreach ($target in ($directoryTargets + $fileTargets)) {
        if (-not (Test-Path -LiteralPath $target)) { continue }
        if ($PSCmdlet.ShouldProcess($target, 'icacls /reset /T')) {
            & icacls $target /reset /T | Out-Null
            Write-Host "  reset  $target"
        }
    }
} else {
    # 一律用 **SID** 授权，不用名字。
    #
    # 名字授权有两个坑：中文/英文系统上同一个组的显示名不同；更隐蔽的是
    # icacls 在受限令牌下加**组**授权时会打印 "Successfully processed"、退出码 0，
    # 却根本不把 ACE 写进去（实测：BUILTIN\Administrators 与 *S-1-5-32-544 都如此）。
    # 既然 icacls 的输出不可信，脚本就必须自己回读确认（见下方 RequiredPrincipals 核验）。
    $required = [ordered]@{
        '运行博客的账号'          = $accountSid
        'NT AUTHORITY\SYSTEM'    = 'S-1-5-18'
        'BUILTIN\Administrators' = 'S-1-5-32-544'
    }

    Write-Host "保留访问权（按 SID）："
    foreach ($label in $required.Keys) { Write-Host "  $label = $($required[$label])" }
    Write-Host "移除：继承来的 NT AUTHORITY\Authenticated Users / BUILTIN\Users"

    $grantArgs = @()
    foreach ($sid in $required.Values) { $grantArgs += "*${sid}:(OI)(CI)F" }

    Write-Host "`n收紧目录（可继承的 ACE）："
    foreach ($target in $directoryTargets) {
        if (-not (Test-Path -LiteralPath $target)) {
            Write-Verbose "跳过（不存在）：$target"
            continue
        }
        if ($PSCmdlet.ShouldProcess($target, '移除继承并只授权属主/SYSTEM/Administrators')) {
            # (OI)(CI) 让 ACE 被文件与子目录继承——这是新文件不再重新变宽松的关键。
            # ⚠️ 只能用在**目录**上；用在文件上会得到一个空 DACL（把文件锁死）。
            $out = & icacls $target /inheritance:r /grant:r @grantArgs 2>&1
            if ($LASTEXITCODE -ne 0) {
                Write-Warning "  FAIL   $target"
                $out | ForEach-Object { Write-Warning "         $_" }
            } else {
                Write-Host "  ok     $target"
            }
        }
    }

    Write-Host "`n文件（恢复为继承父目录，不自己设 ACE）："
    foreach ($target in $fileTargets) {
        if (-not (Test-Path -LiteralPath $target)) {
            Write-Verbose "跳过（不存在）：$target"
            continue
        }
        if ($PSCmdlet.ShouldProcess($target, 'icacls /reset（继承已收紧的父目录）')) {
            $out = & icacls $target /reset 2>&1
            if ($LASTEXITCODE -ne 0) {
                Write-Warning "  FAIL   $target"
                $out | ForEach-Object { Write-Warning "         $_" }
            } else {
                Write-Host "  ok     $target"
            }
        }
    }
}

Write-Host "`n核验（目标是「已收紧**且**仍可用」，不只是「没有宽松主体」）："
$problems = 0
foreach ($target in $verifyTargets) {
    if (-not (Test-Path -LiteralPath $target)) { continue }
    $found = Test-TargetHealth -Path $target -AccountSid $accountSid -RequiredPrincipals $required
    if ($found.Count -gt 0) {
        $problems++
        Write-Warning "  $target"
        $found | ForEach-Object { Write-Warning "      - $_" }
    } else {
        Write-Host "  ok     $target"
    }
}

if ($problems -gt 0) {
    if ($WhatIfPreference) {
        # -WhatIf 下什么都没改，所以「仍需处理」是预期结果而不是失败。
        # 这一步的价值在于**如实告诉操作者当前状态**，好让他们知道这次执行确有必要。
        Write-Host "`n（-WhatIf：以上 $problems 个目标当前仍需处理；去掉 -WhatIf 才会实际修改。）"
        exit 0
    }
    Write-Warning "`n有 $problems 个目标未通过核验。"
    Write-Warning '若提示「DACL 为空」，说明文件被锁死了（对所有人拒绝），'
    Write-Warning '可执行 icacls <路径> /reset 恢复为继承父目录的权限，或用 -Revert 整体回滚。'
    Write-Warning '若提示「缺少预期的授权」，说明 icacls 报告了成功却没有写入 ACE；'
    Write-Warning '请用「以管理员身份运行」的 PowerShell 重跑（受限令牌下加组授权会静默失败）。'
    exit 1
}

Write-Host "`n全部目标已收紧。"
Write-Host "回滚：pwsh -File `"$PSCommandPath`" -Revert"
Write-Host @'

提醒：本脚本只保护**已有的**文件与它所在目录的继承链。若日后把数据目录搬到
别处（或从备份恢复到一个权限宽松的位置），需要在那里重新执行一次。

另注：光靠 ACL 并不能替代「密钥轮换」——访问过这些文件的账号仍然可能已经拿到
过内容。R3-OPS-1（git 历史中的 Resend Key）与 settings.json 里的历史哈希
都需要独立的处置。
'@
