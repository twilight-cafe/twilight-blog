#!/usr/bin/env python3
"""create-service.py — 在 Ubuntu 上安全地把 twilight-blog 装成 systemd 开机自启服务。

封装 deploy/README.md 第二节的手工步骤：

  1. 创建系统用户 twilight-blog（nologin，home=/var/lib/twilight-blog）
  2. 创建数据目录 /var/lib/twilight-blog（0700，归服务账号）
  3. 安装二进制到 /usr/local/bin/twilight-blog（root:root 0755）
  4. 准备 /etc/twilight-blog.env（0600 root:root，可选，发信密钥）
  5. 安装单元文件 deploy/twilight-blog.service 到 /etc/systemd/system/
     ——已存在时先备份为 *.bak-<时间戳>，绝不静默覆盖
  6. systemctl daemon-reload && enable --now

安全设计（为什么这样做）：

  - /var/lib/twilight-blog 里的既有数据（blog.db / settings.json / media/）
    一律不读、不写、不删。脚本只做「缺什么补什么」，可安全重复执行。
  - 覆盖旧二进制 / 旧单元文件前先落一份带时间戳的备份，任何一步可手工回退。
  - 安装前校验二进制是 ELF 且 e_machine 与本机架构一致——go build 环境变量
    没传对时会静默编出宿主平台二进制，文件名看着没问题，到目标机器上
    才炸 Exec format error（build.py 已复核，但手工提供的二进制没有）。
  - 替换二进制前若服务正在运行，先 systemctl stop，避免「替换后仍在跑旧
    代码」的错觉；结束时恢复启动状态。
  - 所有外部命令走参数列表（不用 shell=True），失败即停并给出下一步提示。

用法（需 root）：

  sudo python3 create-service.py                 # 交互式：先展示计划，确认后执行
  sudo python3 create-service.py --yes           # 免确认
  sudo python3 create-service.py --dry-run       # 只打印计划，不做任何修改
  sudo python3 create-service.py --binary ./blog --env-file ./twilight-blog.env

二进制的查找顺序：--binary > /usr/local/bin/twilight-blog（视为已装好，跳过安装）
> 仓库内 twilight-blog/dist/twilight-blog-linux-<本机架构>。
"""

from __future__ import annotations

import argparse
import os
import platform
import shutil
import socket
import subprocess
import sys
import time
from pathlib import Path

SERVICE_NAME = "twilight-blog"
SERVICE_USER = "twilight-blog"
# 固定路径用字符串常量：Path 的 str() 在 Windows 上会变反斜杠，
# 而这些值要插进文本、与单元文件内容比对、传给系统命令，必须恒为 POSIX 形式。
DATA_DIR_STR = "/var/lib/twilight-blog"
BIN_STR = "/usr/local/bin/twilight-blog"
UNIT_STR = f"/etc/systemd/system/{SERVICE_NAME}.service"
ENV_STR = "/etc/twilight-blog.env"

DATA_DIR = Path(DATA_DIR_STR)
BIN_PATH = Path(BIN_STR)
UNIT_PATH = Path(UNIT_STR)
ENV_PATH = Path(ENV_STR)

# ELF e_machine 取值（Linux 头文件 elf-em.h）
EM_X86_64 = 62
EM_AARCH64 = 183

SCRIPT_DIR = Path(__file__).resolve().parent

FATAL_HINTS = {
    "useradd": "请确认系统已安装 passwd 包（Ubuntu 默认自带）。",
    "systemctl": "本脚本只支持 systemd；容器部署请看 deploy/README.md 第五节。",
}


def info(msg: str) -> None:
    print(f"  {msg}")


def warn(msg: str) -> None:
    print(f"  ⚠ {msg}")


def fatal(msg: str, hint: str | None = None) -> "NoReturn":  # type: ignore[name-defined]
    print(f"\n✘ {msg}", file=sys.stderr)
    if hint:
        print(f"  提示：{hint}", file=sys.stderr)
    sys.exit(1)


def run(cmd: list[str], *, dry_run: bool = False, check_output: bool = False,
        may_fail: bool = False) -> subprocess.CompletedProcess | None:
    """执行外部命令。dry_run 时只打印；只读探测（check_output/may_fail）始终真跑。"""
    printable = " ".join(str(c) for c in cmd)
    if dry_run and not (check_output or may_fail):
        info(f"[dry-run] {printable}")
        return None
    try:
        proc = subprocess.run(
            cmd, text=True, capture_output=True,
        )
    except FileNotFoundError:
        fatal(f"找不到命令：{cmd[0]}", FATAL_HINTS.get(cmd[0]))
    if proc.returncode != 0 and not may_fail:
        detail = (proc.stderr or proc.stdout or "").strip().splitlines()
        tail = detail[-1] if detail else f"退出码 {proc.returncode}"
        fatal(f"命令失败：{printable}\n  {tail}")
    return proc


def host_elf_machine() -> int:
    m = platform.machine().lower()
    if m in ("x86_64", "amd64"):
        return EM_X86_64
    if m in ("aarch64", "arm64"):
        return EM_AARCH64
    fatal(f"不支持的宿主架构：{platform.machine()}（本脚本面向 Ubuntu amd64/arm64）")


def validate_linux_binary(path: Path) -> str:
    """校验二进制是 64 位 ELF 且架构与本机匹配，返回人类可读的架构描述。

    读文件头就地判断，不依赖 file/readelf 是否安装。"""
    try:
        header = path.open("rb").read(20)
    except OSError as e:
        fatal(f"无法读取二进制 {path}：{e}")
    if len(header) < 20 or header[:4] != b"\x7fELF":
        fatal(f"{path} 不是 ELF 文件——它很可能不是 Linux 产物，"
              f"请用 build.py 重新产出或用 --binary 指定正确文件")
    ei_class = header[4]
    if ei_class != 2:
        fatal(f"{path} 是 32 位 ELF，本机期望 64 位")
    e_machine = int.from_bytes(header[18:20], "little")
    expected = host_elf_machine()
    names = {EM_X86_64: "x86-64", EM_AARCH64: "aarch64"}
    if e_machine != expected:
        fatal(f"{path} 是 {names.get(e_machine, e_machine)} 架构，"
              f"与本机 {names[expected]} 不匹配——环境变量传错时会静默编出"
              f"宿主平台二进制，请用 build.py 重新构建")
    return names[e_machine]


def validate_unit_source(unit_src: Path, unit_text: str) -> None:
    """校验待安装单元文件的**来源**与**内容**，不通过即终止。

    这个脚本以 root 运行，并会把给定文件装成 /etc/systemd/system/*.service ——
    也就是说，一次「装哪个文件」的判断失误就等于把 root 执行权交给别人。
    原先这里只检查了两件事：文件存在、ExecStart 不是预期值就打一条 warn。
    于是出现一条本地提权路径：任何非特权用户把单元文件的 User/Group 改成 root
    （ExecStart 保持不变，连警告都不触发）放在自己可写的目录里，再等运维执行
    `sudo python3 deploy/create-service.py --unit /tmp/那个文件`，装出来的就是一个
    以 root 运行的站点服务。

    三条检查，各自对应一种失败方式：

      1. 文件与**父目录**都不能是组/全局可写。只查文件不够：父目录可写意味着
         检查与执行之间文件可以被换掉（TOCTOU）。
      2. User= 必须存在且不是 root。unit 里不写 User= 时 systemd 默认就是 root，
         所以「缺失」与「写成 root」要一并拒绝。
      3. Group= 同理不能是 root，避免服务进程属于 root 组。
    """
    for target, what in ((unit_src, "单元文件"), (unit_src.parent, "单元文件所在目录")):
        try:
            mode = target.stat().st_mode
        except OSError as e:
            fatal(f"无法读取{what} {target}：{e}")
        if mode & 0o022:
            fatal(
                f"{what} {target} 对组或其他用户可写（权限 {mode & 0o777:04o}）",
                "请把它放到仅 root 与属主可写的目录（例如仓库内的 deploy/），"
                "并 chmod 去掉组/其他用户的写位后重试。"
                "以 root 安装可被他人改写的单元文件等于交出 root 执行权。",
            )

    validate_unit_identity(unit_text)


def validate_unit_identity(unit_text: str) -> None:
    """检查单元文件里的 User=/Group=，不通过即终止。

    与 validate_unit_source 分开，是为了让这部分**不依赖 POSIX 权限语义**、
    因此可以被跨平台地单独验证（权限位那部分只能在 Linux 上测）。

    规则：User= 必须存在且不是 root。unit 里不写 User= 时 systemd 默认就是 root，
    所以「缺失」与「写成 root」要一并拒绝；Group= 是 root 时告警。
    """
    user_value = unit_directive(unit_text, "User")
    if user_value is None:
        fatal(
            "单元文件里没有 User= —— systemd 默认以 root 运行，本脚本拒绝安装",
            f"请使用 deploy/{SERVICE_NAME}.service（它带有 User={SERVICE_USER}），"
            f"或自行在单元文件里写明 User={SERVICE_USER}。",
        )
    if user_value in ("root", "0"):
        fatal(
            "单元文件的 User= 是 root —— 本脚本拒绝把站点装成 root 服务",
            f"站点只需要读写自己的数据目录，请改成 User={SERVICE_USER}。",
        )
    if user_value != SERVICE_USER:
        warn(f"单元文件的 User={user_value} 不是 {SERVICE_USER}；"
             f"请确认该账号确实存在且只能访问本站数据目录。")

    group_value = unit_directive(unit_text, "Group")
    if group_value in ("root", "0"):
        warn(f"单元文件的 Group=root；建议改成 Group={user_value}，"
             f"避免服务进程无谓地属于 root 组。")


def unit_directive(unit_text: str, name: str) -> str | None:
    """从单元文件里取出某个指令的值（取最后一次出现，与 systemd 的语义一致）。

    只做简单的按行解析，不引入 configparser：单元文件不是 INI，
    而我们要判断的只是 User/Group 这两个单值指令。"""
    found: str | None = None
    for line in unit_text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#") or stripped.startswith(";"):
            continue
        key, sep, value = stripped.partition("=")
        if sep and key.strip() == name:
            found = value.strip()
    return found


def find_binary(cli_value: str | None) -> Path | None:
    """返回待安装的二进制路径；None 表示系统里已装好（跳过安装步）。"""
    if cli_value:
        p = Path(cli_value).expanduser().resolve()
        if not p.is_file():
            fatal(f"--binary 指定的文件不存在：{p}")
        return p
    if BIN_PATH.is_file():
        return None
    arch = {EM_X86_64: "amd64", EM_AARCH64: "arm64"}[host_elf_machine()]
    candidate = SCRIPT_DIR.parent / "twilight-blog" / "dist" / f"{SERVICE_NAME}-linux-{arch}"
    if candidate.is_file():
        return candidate
    fatal(f"找不到二进制：{BIN_STR} 不存在，dist/ 里也没有 "
          f"{candidate.name}。请先运行 python3 build.py，或用 --binary 指定")


def systemctl(*args: str) -> subprocess.CompletedProcess:
    return run(["systemctl", *args], dry_run=False, may_fail=True)


def is_active() -> bool:
    proc = systemctl("is-active", SERVICE_NAME)
    return proc is not None and proc.returncode == 0 and proc.stdout.strip() == "active"


def ts() -> str:
    return time.strftime("%Y%m%d-%H%M%S")


def main() -> None:
    ap = argparse.ArgumentParser(
        description="在 Ubuntu 上安装 twilight-blog 的 systemd 开机自启服务（需 root）")
    ap.add_argument("--binary", help="待安装的 Linux 二进制路径（默认自动查找）")
    ap.add_argument("--env-file", help="发信密钥文件，将安装为 /etc/twilight-blog.env (0600)")
    ap.add_argument("--unit", help=f"单元文件路径（默认 {SCRIPT_DIR / (SERVICE_NAME + '.service')}）")
    ap.add_argument("--no-start", action="store_true", help="只 enable 不启动，稍后手工 start")
    ap.add_argument("--yes", "-y", action="store_true", help="跳过执行前的确认提示")
    ap.add_argument("--dry-run", action="store_true", help="只打印将执行的操作，不做任何修改")
    args = ap.parse_args()

    dry = args.dry_run
    dry_suffix = "（dry-run，以下均为计划）" if dry else ""

    # ── 预检：环境 ──────────────────────────────────────────────────
    if sys.platform != "linux":
        fatal(f"本脚本只能在 Linux 上运行（当前 {sys.platform}）")
    if os.geteuid() != 0:
        fatal("需要 root 权限", "用 sudo python3 create-service.py 重新运行")
    proc = run(["cat", "/etc/os-release"], check_output=True)
    if proc is None or "ubuntu" not in proc.stdout.lower():
        warn("未在 /etc/os-release 里识别到 Ubuntu；脚本兼容多数 systemd 发行版，继续。")
    if not Path("/run/systemd/system").exists():
        fatal("systemd 未在运行", FATAL_HINTS["systemctl"])

    # ── 预检：材料 ──────────────────────────────────────────────────
    unit_src = Path(args.unit).resolve() if args.unit else SCRIPT_DIR / f"{SERVICE_NAME}.service"
    if not unit_src.is_file():
        fatal(f"找不到单元文件：{unit_src}", "请在 deploy/ 目录下运行，或用 --unit 指定")
    unit_text = unit_src.read_text(encoding="utf-8")
    validate_unit_source(unit_src, unit_text)
    if f"ExecStart={BIN_STR}" not in unit_text:
        warn(f"单元文件的 ExecStart 不是 {BIN_STR}，与脚本固定的安装路径不一致，请先核对单元文件。")

    binary = find_binary(args.binary)
    binary_desc = "（已在系统目录，跳过安装）" if binary is None else str(binary)
    if binary is not None:
        validate_linux_binary(binary)

    env_src = Path(args.env_file).expanduser().resolve() if args.env_file else None
    if env_src is not None and not env_src.is_file():
        fatal(f"--env-file 指定的文件不存在：{env_src}")

    user_exists = run(["getent", "passwd", SERVICE_USER],
                      check_output=True, may_fail=True).returncode == 0
    unit_installed = UNIT_PATH.is_file()
    was_active = is_active() if unit_installed else False
    env_exists = ENV_PATH.is_file()

    # 端口占用检查：settings.json 里记录了应用端口，被别的进程占着会导致
    # 服务起来但一直 restart。只提示，不阻断。
    settings = DATA_DIR / "settings.json"
    if settings.is_file():
        try:
            import json
            port = json.loads(settings.read_text(encoding="utf-8")).get("port")
            if isinstance(port, int):
                with socket.socket() as s:
                    s.settimeout(1)
                    occupied = s.connect_ex(("127.0.0.1", port)) == 0
                if occupied and not was_active:
                    warn(f"端口 {port} 已被其他进程占用，而服务当前未运行；"
                         f"启动可能失败，可用 journalctl -u {SERVICE_NAME} 排查。")
        except (OSError, ValueError):
            pass

    # ── 展示计划 ────────────────────────────────────────────────────
    print(f"\n=== twilight-blog 服务安装计划{dry_suffix} ===")
    info(f"单元文件 : {unit_src} -> {UNIT_STR}"
         + ("（已存在，将先备份）" if unit_installed else ""))
    info(f"二进制   : {binary_desc}"
         + (f" -> {BIN_STR}（已存在，将先备份）"
            if binary is not None and BIN_PATH.is_file() else ""))
    info(f"服务账号 : {SERVICE_USER}" + ("（已存在，跳过）" if user_exists else "（将创建）"))
    info(f"数据目录 : {DATA_DIR_STR}" + ("（已存在，内容不动）" if DATA_DIR.is_dir() else "（将创建，0700）"))
    info(f"环境变量 : {ENV_STR}"
         + ("（已存在，不动）" if env_exists
            else f"（将{'从 ' + str(env_src) + ' ' if env_src else ''}创建，0600 root）"))
    info(f"启动     : {'只 enable 不 start' if args.no_start else 'enable --now'}"
         + (f"（当前正在运行，替换二进制前会先 stop）" if was_active else ""))

    if dry:
        print("\ndry-run 结束，未做任何修改。")
        return
    if not args.yes:
        try:
            answer = input("\n执行以上操作？[y/N] ").strip().lower()
        except (EOFError, KeyboardInterrupt):
            answer = ""
        if answer not in ("y", "yes"):
            print("已取消，未做任何修改。")
            sys.exit(2)

    print()

    # ── 1. 服务账号 ────────────────────────────────────────────────
    if not user_exists:
        run(["useradd", "--system",
             "--home-dir", DATA_DIR_STR,
             "--shell", "/usr/sbin/nologin", SERVICE_USER], dry_run=dry)
        info(f"已创建系统用户 {SERVICE_USER}（nologin）")
    else:
        info(f"系统用户 {SERVICE_USER} 已存在，跳过")

    # ── 2. 数据目录 ────────────────────────────────────────────────
    # install -d 是幂等的：已存在时只确保属主与权限，不碰内容。
    run(["install", "-d", "-o", SERVICE_USER, "-g", SERVICE_USER,
         "-m", "0700", DATA_DIR_STR], dry_run=dry)
    info(f"数据目录就绪：{DATA_DIR_STR}（0700，属主 {SERVICE_USER}）")

    # ── 3. 二进制 ──────────────────────────────────────────────────
    if binary is not None:
        if binary.resolve() == BIN_PATH.resolve():
            info(f"二进制已在 {BIN_STR}，跳过安装")
        else:
            if BIN_PATH.is_file():
                backup = f"{BIN_STR}.bak-{ts()}"
                run(["cp", "-p", BIN_STR, backup], dry_run=dry)
                info(f"旧二进制已备份为 {backup}")
            if was_active:
                run(["systemctl", "stop", SERVICE_NAME], dry_run=dry)
                info("服务已停止（替换二进制）")
            run(["install", "-o", "root", "-g", "root", "-m", "0755",
                 str(binary), BIN_STR], dry_run=dry)
            info(f"二进制已安装：{BIN_STR}")

    # ── 4. 环境变量文件 ────────────────────────────────────────────
    if env_exists:
        info(f"{ENV_STR} 已存在，不动（密钥如需更换请手工编辑）")
    elif env_src is not None:
        run(["install", "-m", "0600", "-o", "root", "-g", "root",
             str(env_src), ENV_STR], dry_run=dry)
        info(f"密钥文件已安装：{ENV_STR}（0600 root:root）")
    else:
        run(["install", "-m", "0600", "-o", "root", "-g", "root",
             "/dev/null", ENV_STR], dry_run=dry)
        info(f"已创建空的 {ENV_STR}（未配置邮件 = 发信功能关闭，属合法状态）")

    # ── 5. 单元文件 ────────────────────────────────────────────────
    if unit_installed:
        backup = f"{UNIT_STR}.bak-{ts()}"
        run(["cp", "-p", UNIT_STR, backup], dry_run=dry)
        info(f"旧单元文件已备份为 {backup}")
    run(["install", "-m", "0644", str(unit_src), UNIT_STR], dry_run=dry)
    info(f"单元文件已安装：{UNIT_STR}")

    # ── 6. 启用 ────────────────────────────────────────────────────
    run(["systemctl", "daemon-reload"], dry_run=dry)
    if args.no_start:
        run(["systemctl", "enable", SERVICE_NAME], dry_run=dry)
        info(f"已 enable（未启动；稍后手工 systemctl start {SERVICE_NAME}）")
        if was_active:
            warn(f"服务原本正在运行且已被 stop，而指定了 --no-start；"
                 f"站点当前是停机状态，记得手工 start。")
    else:
        run(["systemctl", "enable", "--now", SERVICE_NAME], dry_run=dry)
        info(f"已 enable --now")
    if binary is not None and was_active and not args.no_start:
        # enable --now 对已 active 的单元不会重启，需要显式 restart 载入新二进制。
        run(["systemctl", "restart", SERVICE_NAME], dry_run=dry)
        info("服务已 restart 以载入新二进制")

    # ── 完成报告 ───────────────────────────────────────────────────
    print(f"\n=== 完成 ===")
    if not dry and not args.no_start:
        state = systemctl("is-active", SERVICE_NAME)
        state_text = state.stdout.strip() if state and state.stdout else "未知"
        info(f"服务状态：{state_text}")
        if state_text != "active":
            warn(f"服务未处于 active；查看日志：journalctl -u {SERVICE_NAME} -e --no-pager")
    info(f"日志：journalctl -u {SERVICE_NAME} -f")
    if not (DATA_DIR / "blog.db").is_file():
        warn(f"检测到是全新数据目录：首次请打开 http://127.0.0.1:8080/install "
             f"创建管理员（/install 在设置管理员前对任何能访问站点的人开放）。")
    info("Meilisearch 首次启动会自动下载（数百 MB）；生产环境建议按 README 第三节预置。")


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        print("\n已中断。", file=sys.stderr)
        sys.exit(130)
