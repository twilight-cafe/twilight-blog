#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""构建 twilight-blog：先把前端产物嵌进 Go 源码，再编译各平台二进制。

三步流程：
  1. 构建前端项目（twilight-blog-web / twilight-web）；
  2. 把生成的 ``dist`` 拷进 ``twilight-blog/internal/view/web``；
  3. 编译 Go 二进制——这一步会产出**多个平台**的产物。

前端资源由 ``//go:embed`` 嵌进 Go 二进制，所以第 2 步必须早于第 3 步，
而且第 3 步里所有平台共用同一份嵌入结果（前端只构建一次）。

## 目标平台

在 **Windows** 上运行时默认编译三份产物（这正是这个脚本存在的主要理由：
一次命令就能拿到可发布的 Windows 包与 Linux 部署包）：

    windows/amd64   本机可执行，同时复制到 twilight-blog/main.exe（兼容旧用法）
    linux/amd64     交叉编译
    linux/arm64     交叉编译

在**其它宿主**上默认只编译本机平台（保持改动前的行为）。想覆盖用
``--targets``，例如 ``--targets linux/amd64,linux/arm64``。

产物统一落在 ``twilight-blog/dist/``，命名 ``twilight-blog-<goos>-<goarch>[.exe]``。
该目录已被仓库根 ``.gitignore`` 的 ``dist/`` 规则忽略。

## 为什么交叉编译不需要 CGO

SQLite 用的是纯 Go 实现 ``modernc.org/sqlite``，全部依赖都没有 cgo。
因此所有目标都显式设 ``CGO_ENABLED=0``：交叉编译才可行，产出的 Linux 二进制
是**静态链接**的（不依赖 glibc），可以直接丢进 scratch/alpine 或最小发行版。

## 为什么编译完还要校验产物

``go build`` 如果因为环境变量没传对而忽略了 ``GOARCH``，会**静默**产出一个
本机架构的二进制——文件名看着对，拿到目标机器上才报 ``Exec format error``。
所以每个产物都要读文件头确认真的对得上：PE 的 ``MZ`` + COFF machine 字段，
ELF 的 ``\\x7fELF`` + e_machine 字段。这一步几乎零成本，却能挡住一类很贵的错误。

用法::

    python3 build.py                     # 按宿主平台选默认目标
    python3 build.py --list              # 只列出会构建哪些目标
    python3 build.py --targets linux/amd64,linux/arm64
    python3 build.py --host-only         # 只构建本机平台
    python3 build.py --skip-frontend     # 跳过前端（嵌入资源已是最新时）
"""

from __future__ import annotations

import argparse
import os
import re
import shlex
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path

ROOT = Path(__file__).resolve().parent
GO_PROJECT = ROOT / "twilight-blog"
GO_WEB_EMBED_DIR = GO_PROJECT / "internal" / "view" / "web"
DIST_DIR = GO_PROJECT / "dist"

# 旧版脚本把二进制写在 Go 项目根目录下的这个名字，仓库里多份文档
# （HANDOFF.md、ADMIN-CREDENTIALS-HANDOFF.md）都按 `main.exe admin set-password`
# 这样引用它。保留这个落点是刻意的兼容措施，不要顺手删掉。
LEGACY_WINDOWS_OUTPUT = GO_PROJECT / "main.exe"

# The front-end project may be named twilight-blog-web in other checkouts;
# this repository currently uses twilight-web.
FRONTEND_DIR_NAMES = ("twilight-blog-web", "twilight-web")

# 产物文件名的前缀。带前缀是为了让人一眼看出这是哪个项目的包，
# 解压到服务器上时不会和别的东西撞名。
ARTIFACT_PREFIX = "twilight-blog"


@dataclass(frozen=True)
class Target:
    """一个构建目标：GOOS/GOARCH 的组合。"""

    goos: str
    goarch: str

    @property
    def slug(self) -> str:
        return f"{self.goos}-{self.goarch}"

    @property
    def extension(self) -> str:
        # 只有 Windows 需要后缀；Linux/macOS 不加，否则没法直接执行。
        return ".exe" if self.goos == "windows" else ""

    @property
    def artifact_name(self) -> str:
        return f"{ARTIFACT_PREFIX}-{self.slug}{self.extension}"

    def __str__(self) -> str:
        return f"{self.goos}/{self.goarch}"

    # 有了 __str__ 还不够：f-string 里的对齐格式（{target:<18}）走的是
    # __format__，而 dataclass 不会自动从 __str__ 推导出来——不定义就会在
    # 打印汇总表时抛 TypeError。委托给 str(self) 让两者行为一致。
    def __format__(self, spec: str) -> str:
        return format(str(self), spec)


WINDOWS_AMD64 = Target("windows", "amd64")
LINUX_AMD64 = Target("linux", "amd64")
LINUX_ARM64 = Target("linux", "arm64")

# Windows 宿主的默认目标集合。
#
# 为什么把 Linux 两个架构也放进默认值：这个项目的部署目标是 Linux
# （见 deploy/twilight-blog.service），而开发机是 Windows。默认就把跨平台产物
# 一起产出来，可以少一次「在 Windows 上手动敲 GOOS=linux go build」的机会——
# 那种手敲很容易漏掉 CGO_ENABLED=0 或 GOARCH，而漏掉的后果又很隐蔽。
WINDOWS_DEFAULT_TARGETS: tuple[Target, ...] = (WINDOWS_AMD64, LINUX_AMD64, LINUX_ARM64)

# ELF e_machine 的取值。用来断言交叉编译真的换成了目标架构。
ELF_MACHINE = {"amd64": 0x3E, "arm64": 0xB7, "386": 0x03, "arm": 0x28, "riscv64": 0xF3}
# PE COFF machine 的取值。
PE_MACHINE = {"amd64": 0x8664, "arm64": 0xAA64, "386": 0x014C, "arm": 0x01C4}


def fail(message: str) -> None:
    print(f"[build] error: {message}", file=sys.stderr)
    raise SystemExit(1)


def find_command(name: str) -> str | None:
    """Find an executable, including Windows cmd/bat shims."""
    found = shutil.which(name)
    if found:
        return found

    if os.name == "nt":
        for extension in (".cmd", ".bat", ".exe"):
            found = shutil.which(name + extension)
            if found:
                return found
    return None


def run(command: list[str], cwd: Path, env: dict[str, str] | None = None) -> None:
    rendered = shlex.join(command)
    print(f"[build] run: {rendered} (cwd: {cwd})", flush=True)
    try:
        subprocess.run(command, cwd=str(cwd), check=True, env=env)
    except FileNotFoundError:
        fail(f"command not found: {command[0]}")
    except subprocess.CalledProcessError as exc:
        fail(f"command failed with exit code {exc.returncode}: {rendered}")


def find_frontend_dir() -> Path:
    for name in FRONTEND_DIR_NAMES:
        candidate = ROOT / name
        if candidate.is_dir() and (candidate / "package.json").is_file():
            return candidate
    fail(
        "cannot find the front-end project; expected one of: "
        + ", ".join(FRONTEND_DIR_NAMES)
    )


def find_package_manager(project_dir: Path) -> str:
    # Respect the lockfile used by the project when its package manager exists.
    if (project_dir / "pnpm-lock.yaml").is_file():
        pnpm = find_command("pnpm")
        if pnpm:
            return pnpm
    if (project_dir / "package-lock.json").is_file():
        npm = find_command("npm")
        if npm:
            return npm

    for name in ("pnpm", "npm"):
        found = find_command(name)
        if found:
            return found

    fail("neither pnpm nor npm was found on PATH")


def ensure_dependencies(project_dir: Path) -> str:
    """Install node dependencies if needed, and return the package manager."""
    manager = find_package_manager(project_dir)
    if not (project_dir / "node_modules").is_dir():
        print(f"[build] installing dependencies in {project_dir}")
        run([manager, "install"], cwd=project_dir)
    return manager


def build_node_project(
    project_dir: Path,
    label: str,
    prebuilt_output: Path | None = None,
) -> None:
    """Run ``<manager> run build`` for a Node project.

    ``prebuilt_output`` allows the caller to fall back to an already built
    output when Node child-process creation is blocked (for example in a
    restricted Windows sandbox where ``spawn EPERM`` is reported by esbuild).
    The fallback is intentionally narrow: it only applies for EPERM errors and
    only when the expected output already exists.
    """
    manager = ensure_dependencies(project_dir)
    command = [manager, "run", "build"]
    rendered = shlex.join(command)
    print(f"[build] run: {rendered} (cwd: {project_dir})", flush=True)

    try:
        result = subprocess.run(
            command,
            cwd=str(project_dir),
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
    except FileNotFoundError:
        fail(f"command not found: {command[0]}")

    if result.stdout:
        sys.stdout.write(result.stdout)
    if result.stderr:
        sys.stderr.write(result.stderr)

    if result.returncode == 0:
        return

    combined = (result.stdout or "") + "\n" + (result.stderr or "")
    if prebuilt_output is not None and prebuilt_output.is_file() and "EPERM" in combined:
        print(
            "[build] warning: front-end build could not spawn Node child "
            f"processes; using existing build output: {prebuilt_output}",
            file=sys.stderr,
        )
        return

    # 前端构建失败时给出可执行的下一步。这里刻意**不**扩大回退范围：
    # 只有 EPERM 那种「环境不让 spawn 子进程」才允许用陈旧产物，
    # 真正的编译错误必须硬失败——否则会安安静静地打出一个前端是旧版本的包，
    # 而那种问题要到线上才发现。
    #
    # 提示信息用英文：本脚本的**运行输出**一律保持 ASCII，而注释与文档字符串
    # 用中文。这不是随意规定——Python 在 Windows 上按控制台代码页（通常是 936）
    # 编码 stdout，同一段中文写进 CI 日志或重定向到文件后，换个 UTF-8 的查看器
    # 就是乱码。注释只在编辑器里读，不受影响；输出要经得起管道与日志。
    print(
        "[build] hint: the front-end build failed. The two usual causes are:\n"
        f"        1) node_modules is incomplete or stale -- run `{Path(manager).name} install` "
        f"in {project_dir} and retry;\n"
        "        2) you only need the Go binaries and the currently embedded assets are "
        "up to date -- add --skip-frontend.",
        file=sys.stderr,
    )
    fail(f"command failed with exit code {result.returncode}: {rendered}")


def build_frontend(frontend_dir: Path) -> Path:
    dist_dir = frontend_dir / "dist"
    build_node_project(
        frontend_dir,
        "front-end",
        prebuilt_output=dist_dir / "index.html",
    )
    if not (dist_dir / "index.html").is_file():
        fail(f"front-end build output not found: {dist_dir / 'index.html'}")
    return dist_dir


def clear_directory(path: Path) -> None:
    """清掉一个可重建的目录，保证它**不再存在**。

    调用方需要的不变量是「path 不存在」，而不是「path 被删掉」——紧随其后的
    ``rename`` 只要求前者，所以删不掉时改名挪开同样满足契约。

    为什么不要求「必须删掉」：目录里只要有一个句柄没放（Windows 上杀毒软件、
    编辑器、上一轮构建残留都会），``rmtree`` 就整体失败；而这里要清的是**上一轮
    的前端产物**，纯粹是中间物。把整条构建链卡在它身上，代价远大于留着它。

    真踩过：受限环境下批量删除被策略拦下（条目数超阈值），``shutil.rmtree`` 抛
    OSError，于是 ``python build.py`` 在**前端已经构建成功之后**失败，而日志里只有
    一句「拒绝批量删除」，跟「前端产物」看不出任何关系。

    挪走时给出警告而不是静默丢弃：那些目录会一直占着磁盘，用户需要知道去哪清。
    """
    if not path.exists():
        return
    try:
        shutil.rmtree(path)
        return
    except OSError as exc:
        relocated = path.with_name(f"{path.name}.stale-{time.strftime('%Y%m%d-%H%M%S')}")
        try:
            path.rename(relocated)
        except OSError as rename_exc:
            fail(f"cannot clear {path} ({exc}); relocating it also failed: {rename_exc}")
        print(
            f"[build] warning: could not remove {path} ({exc}); "
            f"moved it to {relocated} -- delete that directory when convenient",
            file=sys.stderr,
        )


# 产物里可能出现的本地资源引用。三种形式各来自一处：
#   - HTML：  <script src="/assets/...">、<link href="/assets/...">
#   - CSS：   background:url(/assets/xxx.woff2)（Vite 重写成根绝对路径）
#   - JS：    import("./fs-promises-xxxx.js")（动态分片，相对当前文件）
REFERENCE_PATTERNS = (
    re.compile(r"""(?:src|href)\s*=\s*["']([^"']+)["']"""),
    re.compile(r"""url\(\s*["']?([^"')]+?)["']?\s*\)"""),
    re.compile(r"""(?:import|from)\s*\(\s*["']([^"']+)["']\s*\)"""),
    re.compile(r"""(?:import|export)\s+(?:[^"';]*?\s+from\s+)?["']([^"']+)["']"""),
)

# 只对「看起来是本地静态资源」的引用做存在性检查。
# 收窄到已知扩展名是有意的：宽泛的字符串匹配会把运行时拼出来的路径也当成引用，
# 从而制造**假失败**——一个会误报的构建守卫比没有守卫更糟。
_ASSET_SUFFIXES = (
    ".js", ".mjs", ".css", ".map",
    ".woff", ".woff2", ".ttf", ".otf", ".eot",
    ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".avif", ".ico",
)

_EXTERNAL_PREFIXES = ("#", "data:", "http:", "https:", "//", "mailto:", "blob:", "javascript:")


def _looks_like_local_asset(ref: str) -> bool:
    if not ref or ref.startswith(_EXTERNAL_PREFIXES):
        return False
    path = ref.split("#", 1)[0].split("?", 1)[0]
    return path.endswith(_ASSET_SUFFIXES)


def verify_embedded_assets(embed_dir: Path) -> None:
    """确认嵌入目录里被引用的每个本地资源都真的存在。

    **为什么需要这道检查**：``vite build`` 在某些条件下会产出一份
    「引用完整、但资源文件不全」的 dist，而且**全程不报错**。本仓库真踩到过：
    依赖树被截断时，产物少了 28 个 KaTeX 字体文件，而 CSS 里 60 条
    ``/assets/...`` 引用只有 32 个文件能对上。这些引用是根绝对路径，
    缺失的文件在运行时会变成浏览器里的 404——现象发生在生产环境，
    构建日志里却没有任何线索，且二进制看起来「构建成功」。

    检查的是**嵌入目录**而不是 ``twilight-web/dist``：真正被 ``//go:embed``
    编进二进制的是前者，而且 ``--skip-frontend`` 这条路径完全跳过前端构建，
    只有在这里才能兜住它（本轮出问题的正是这条路径）。
    """
    missing: list[tuple[str, str]] = []
    for source in sorted(embed_dir.rglob("*")):
        if not source.is_file() or source.suffix not in {".html", ".css", ".js"}:
            continue
        try:
            text = source.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            # 二进制或非 UTF-8 内容：不是文本引用源，跳过。
            continue
        for pattern in REFERENCE_PATTERNS:
            for match in pattern.finditer(text):
                ref = match.group(1).strip()
                if not _looks_like_local_asset(ref):
                    continue
                clean = ref.split("#", 1)[0].split("?", 1)[0]
                # 以 / 开头是相对站点根（即嵌入目录根），否则相对引用它的那个文件。
                target = (embed_dir / clean.lstrip("/")) if clean.startswith("/") else (source.parent / clean)
                if not target.exists():
                    missing.append((source.relative_to(embed_dir).as_posix(), ref))
    if missing:
        detail = "\n".join(f"  - {src} -> {ref}" for src, ref in missing)
        fail(
            f"embedded front-end is incomplete: {len(missing)} referenced asset(s) are missing "
            f"from {embed_dir}:\n{detail}\n"
            "The front-end build referenced files it did not emit (usually because "
            "node_modules lost those files mid-install). Re-install the front-end "
            "dependencies, rebuild, and re-run this script without --skip-frontend."
        )


def embed_frontend_dist(frontend_dir: Path) -> None:
    dist_dir = frontend_dir / "dist"
    if not (dist_dir / "index.html").is_file():
        fail(f"front-end build output not found: {dist_dir / 'index.html'}")

    # Copy into a temporary sibling and swap it in at the end. Removing the live
    # embed directory first means a failure mid-copy leaves the Go sources without
    # the assets that //go:embed requires, breaking every later build until the
    # front-end is rebuilt successfully.
    #
    # 这一点对所有平台共用：嵌入发生在**编译期**，所以三份产物嵌的是同一份
    # 前端资源。不存在「Windows 包和 Linux 包前端不一致」的可能。
    staging = GO_WEB_EMBED_DIR.with_name(GO_WEB_EMBED_DIR.name + ".staging")
    clear_directory(staging)
    staging.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(dist_dir, staging)

    # 在换上去之前就检查：一份不完整的资源不该被扶正，否则它会留在 embed 目录里，
    # 让后续每一次 ``--skip-frontend`` 构建都继续嵌它。
    verify_embedded_assets(staging)

    previous = GO_WEB_EMBED_DIR.with_name(GO_WEB_EMBED_DIR.name + ".previous")
    clear_directory(previous)
    if GO_WEB_EMBED_DIR.exists():
        GO_WEB_EMBED_DIR.rename(previous)
    staging.rename(GO_WEB_EMBED_DIR)
    clear_directory(previous)

    print(f"[build] embedded front-end assets copied to {GO_WEB_EMBED_DIR}")


def find_go() -> str:
    go = find_command("go")
    if not go:
        fail("go was not found on PATH")
    return go


def go_env(base: dict[str, str], target: Target | None = None) -> dict[str, str]:
    """构造 ``go build`` 用的环境变量。

    ``target`` 为 None 表示「不指定平台」，用于 ``go env`` 这类查询（此时不能带
    GOOS/GOARCH，否则查到的是交叉查询结果而不是宿主信息）。
    """
    env = dict(base)
    # 请求不收集也不上传遥测数据。原脚本就有这一行，保留。
    #
    # 注意 `GOTELEMETRY` 在 `go env` 里是**只读的报告值**（模式本身由
    # `go telemetry off` 写进遥测目录），所以这个环境变量能压住的是上传，
    # 压不住「go 想在用户配置目录里建一个 token 文件」这一步。若该目录不可写
    # （受限的 CI 容器、被策略锁住的 %APPDATA%），stderr 上会出现
    # "error acquiring upload token" —— **它不影响构建**，`go build` 照常返回 0。
    # 在普通开发机上不会看到这条。
    env["GOTELEMETRY"] = "off"

    # Use project-local module/cache directories when present. This keeps the
    # build reproducible on machines that already have these populated.
    if (GO_PROJECT / ".gocache").is_dir():
        env["GOCACHE"] = str(GO_PROJECT / ".gocache")
    if (GO_PROJECT / ".modcache").is_dir():
        env["GOMODCACHE"] = str(GO_PROJECT / ".modcache")

    if target is not None:
        env["GOOS"] = target.goos
        env["GOARCH"] = target.goarch
        # 全平台统一关掉 cgo：交叉编译必须如此，而本项目没有任何 cgo 依赖
        # （SQLite 走 modernc.org/sqlite 的纯 Go 实现）。统一之后本机产物与
        # 交叉产物是同一种形态——静态链接、不依赖 glibc。
        env["CGO_ENABLED"] = "0"
    return env


def host_target(go: str) -> Target:
    """问 Go 自己宿主的 GOOS/GOARCH，而不是从 sys.platform 猜。

    Python 的 ``sys.platform`` 与 Go 的 GOOS 命名并不一一对应
    （amd64/x86_64、darwin/macos 之类），让 Go 回答最省事也最准。
    """
    try:
        result = subprocess.run(
            [go, "env", "GOOS", "GOARCH"],
            capture_output=True,
            text=True,
            check=True,
            env=go_env(os.environ),
        )
    except subprocess.CalledProcessError as exc:
        fail(f"go env failed with exit code {exc.returncode}")

    lines = [line.strip() for line in (result.stdout or "").splitlines() if line.strip()]
    if len(lines) != 2:
        fail(f"unexpected `go env GOOS GOARCH` output: {result.stdout!r}")
    return Target(lines[0], lines[1])


def parse_target(spec: str) -> Target:
    """解析 ``goos/goarch`` 形式的命令行目标。"""
    parts = spec.strip().split("/")
    if len(parts) != 2 or not all(parts):
        fail(f"invalid target {spec!r}; expected the form <goos>/<goarch>, e.g. linux/amd64")
    return Target(parts[0], parts[1])


def verify_artifact(path: Path, target: Target) -> None:
    """读文件头确认产物真的属于目标平台。

    这一步挡的是一类很隐蔽的失败：``GOOS``/``GOARCH`` 没传进环境时，``go build``
    **不会报错**，只会安安静静地编译出宿主平台的二进制。文件名看着没问题，
    部署到目标机器上才炸 ``Exec format error``——而那时排查起来会怀疑编译器、
    怀疑工具链，就是不会怀疑构建脚本。

    校验放在这里而不是「相信 go build」是划算的：读 64 个字节的成本可以忽略。
    """
    try:
        with path.open("rb") as handle:
            head = handle.read(0x200)
            if target.goos == "windows":
                # PE 的 e_lfanew 在 0x3C，指向 "PE\\0\\0"，machine 紧跟其后。
                if len(head) < 0x40:
                    fail(f"{path.name}: file is too short to be a PE binary")
                pe_offset = int.from_bytes(head[0x3C:0x40], "little")
                handle.seek(pe_offset)
                pe_header = handle.read(8)

                if head[:2] != b"MZ":
                    fail(f"{path.name}: not a PE binary (missing MZ signature)")
                if pe_header[:4] != b"PE\0\0":
                    fail(f"{path.name}: not a PE binary (missing PE signature)")
                machine = int.from_bytes(pe_header[4:6], "little")
                expected = PE_MACHINE.get(target.goarch)
                if expected is not None and machine != expected:
                    fail(
                        f"{path.name}: declared {target} but the PE machine field is "
                        f"0x{machine:04X} (expected 0x{expected:04X})"
                    )
            else:
                if head[:4] != b"\x7fELF":
                    fail(
                        f"{path.name}: not an ELF binary (missing \\x7fELF signature); "
                        f"did the cross-compilation settings actually reach `go build`?"
                    )
                if head[4] != 2:
                    fail(f"{path.name}: not a 64-bit ELF (EI_CLASS={head[4]})")
                if head[5] != 1:
                    fail(f"{path.name}: unexpected ELF byte order (EI_DATA={head[5]})")
                machine = int.from_bytes(head[18:20], "little")
                expected = ELF_MACHINE.get(target.goarch)
                if expected is not None and machine != expected:
                    fail(
                        f"{path.name}: declared {target} but the ELF e_machine is "
                        f"0x{machine:02X} (expected 0x{expected:02X})"
                    )
    except OSError as exc:
        fail(f"cannot inspect {path}: {exc}")


def build_one(go: str, target: Target, base_env: dict[str, str]) -> Path:
    """编译一个目标平台，返回产物路径。"""
    DIST_DIR.mkdir(parents=True, exist_ok=True)
    output = DIST_DIR / target.artifact_name

    env = go_env(base_env, target)
    run([go, "build", "-o", str(output), "."], cwd=GO_PROJECT, env=env)

    if not output.is_file():
        fail(f"Go build did not produce {output}")
    verify_artifact(output, target)
    return output


def human_size(num_bytes: int) -> str:
    return f"{num_bytes / (1024 * 1024):.1f} MiB"


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Build twilight-blog for one or more platforms.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "On Windows, running this with no arguments builds windows/amd64 plus\n"
            "cross-compiled linux/amd64 and linux/arm64. On any other host it builds\n"
            "the native platform only.\n"
            "\n"
            "Artifacts land in twilight-blog/dist/ (git-ignored)."
        ),
    )
    parser.add_argument(
        "--targets",
        metavar="GOOS/GOARCH[,GOOS/GOARCH...]",
        help="build exactly these targets, overriding the host default "
        "(for example linux/amd64,linux/arm64)",
    )
    parser.add_argument(
        "--host-only",
        action="store_true",
        help="build only the native platform (the behaviour before multi-target support)",
    )
    parser.add_argument(
        "--skip-frontend",
        action="store_true",
        help="skip building and embedding the front-end; use when the embedded assets "
        "are already current (saves one front-end build)",
    )
    parser.add_argument(
        "--list",
        action="store_true",
        help="print the targets that would be built, then exit",
    )
    args = parser.parse_args()

    if args.targets and args.host_only:
        fail("--targets and --host-only cannot be combined")

    go = find_go()
    native = host_target(go)

    if args.targets:
        targets: list[Target] = []
        for spec in args.targets.split(","):
            if not spec.strip():
                continue
            target = parse_target(spec)
            # 去重，但保留用户给的顺序——产物的打印顺序会照着它来。
            if target not in targets:
                targets.append(target)
        if not targets:
            fail("--targets was given but contained no usable target")
    elif args.host_only:
        targets = [native]
    elif os.name == "nt":
        targets = list(WINDOWS_DEFAULT_TARGETS)
    else:
        targets = [native]

    print(f"[build] host: {sys.platform} (go says {native})")
    print(f"[build] targets: {', '.join(str(t) for t in targets)}")
    if args.list:
        for target in targets:
            print(f"  {target:<18} -> {DIST_DIR / target.artifact_name}")
        return

    if not args.skip_frontend:
        frontend_dir = find_frontend_dir()
        print(f"[build] front-end project: {frontend_dir}")
        # No pre-build of twilight-blog-api or InkUI is needed: the front-end aliases
        # both packages to their TypeScript sources (see twilight-web/vite.config.ts).
        build_frontend(frontend_dir)
        embed_frontend_dist(frontend_dir)
    else:
        # 跳过前端时仍然要确认嵌入目录非空，否则 //go:embed 会让编译直接失败，
        # 而那时的报错和「你跳过了前端」之间没有任何线索。
        if not (GO_WEB_EMBED_DIR / "index.html").is_file():
            fail(
                "cannot skip the front-end: the embed directory has no index.html. "
                "Run without --skip-frontend at least once."
            )
        # 这条路径跳过了一切前端构建，因此也正是「资源不完整」最容易溜过去的地方：
        # 编译照样成功，坏掉的资源却进了二进制。这里必须再验一次完整性。
        verify_embedded_assets(GO_WEB_EMBED_DIR)
        print("[build] skipping front-end build; using the existing embedded assets")

    base_env = os.environ.copy()
    results: list[tuple[Target, Path]] = []
    for target in targets:
        print(f"[build] building {target} ...")
        results.append((target, build_one(go, target, base_env)))

    # 兼容旧用法：仓库文档里到处是 `main.exe admin set-password`。
    # 用复制而不是硬链接——`go build` 是「写临时文件再改名」，
    # 硬链接会一直指向被替换掉的旧 inode，也就是永远停在上一版。
    legacy_note = ""
    windows_amd64_output = next((path for target, path in results if target == WINDOWS_AMD64), None)
    if windows_amd64_output is not None:
        shutil.copy2(windows_amd64_output, LEGACY_WINDOWS_OUTPUT)
        legacy_note = str(LEGACY_WINDOWS_OUTPUT)

    print("\n[build] artifacts:")
    for target, path in results:
        print(f"  {target:<18} {path.name:<34} {human_size(path.stat().st_size)}")
    if legacy_note:
        print(f"  {'(legacy copy)':<18} {legacy_note}")
    print("[build] done")


if __name__ == "__main__":
    main()
