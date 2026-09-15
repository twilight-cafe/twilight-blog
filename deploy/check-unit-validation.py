"""一次性验证脚本：检查 create-service.py 新增的单元文件校验是否按预期工作。

用 importlib 从文件路径加载（文件名里带连字符，不能直接 import）。
只调用纯函数，不触碰系统状态——main() 有 __main__ 守卫且需要 root/Linux。

⚠️ 平台说明：`validate_unit_source` 里的权限位检查依赖 POSIX 语义。
Windows 上 Python 的 os.chmod 只能切换只读位，st_mode 由只读属性推导，
任何可写文件都会呈现组/全局可写的样子——所以那两条断言在 Windows 上**跳过**，
必须在 Linux 上复核（脚本本身也只在 Linux 下运行）。
"""
import importlib.util
import os
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
POSIX = os.name == "posix"
SCRATCH = HERE / ".unit-validation-scratch"

SPEC = importlib.util.spec_from_file_location("create_service", HERE / "create-service.py")
mod = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(mod)

failures = []
skipped = []


def check(name, fn, expect_exit):
    try:
        fn()
    except SystemExit as exc:
        code = exc.code if isinstance(exc.code, int) else 2
        if expect_exit:
            print(f"  PASS  {name} -> rejected (exit={code})")
        else:
            failures.append(name)
            print(f"  FAIL  {name} -> unexpectedly rejected (exit={code})")
        return
    if expect_exit:
        failures.append(name)
        print(f"  FAIL  {name} -> should have been rejected but passed")
    else:
        print(f"  PASS  {name} -> accepted")


print("=== unit_directive parsing ===")
text = "User=a\nGroup=b\n# User=root\n\nUser=last-wins\n"
got = mod.unit_directive(text, "User")
print(f"  last-wins override: User -> {got!r} (want 'last-wins'; comments ignored)")
if got != "last-wins":
    failures.append("unit_directive-last-wins")
if mod.unit_directive(text, "Group") != "b":
    failures.append("unit_directive-group")
if mod.unit_directive(text, "ExecStart") is not None:
    failures.append("unit_directive-missing")
print(f"  Group -> {mod.unit_directive(text, 'Group')!r}, "
      f"missing directive -> {mod.unit_directive(text, 'ExecStart')!r}")

print("\n=== validate_unit_identity (platform independent) ===")
SCRATCH.mkdir(exist_ok=True)
as_root = SCRATCH / "root.service"
as_root.write_text('[Service]\nUser=root\nExecStart=/usr/local/bin/twilight-blog\n', encoding="utf-8")
check("User=root", lambda: mod.validate_unit_identity(as_root.read_text()), True)

as_zero = SCRATCH / "zero.service"
as_zero.write_text('[Service]\nUser=0\n', encoding="utf-8")
check("User=0", lambda: mod.validate_unit_identity(as_zero.read_text()), True)

no_user = SCRATCH / "nouser.service"
no_user.write_text('[Service]\nExecStart=/usr/local/bin/twilight-blog\n', encoding="utf-8")
check("missing User= (systemd would run as root)",
      lambda: mod.validate_unit_identity(no_user.read_text()), True)

indented = SCRATCH / "indented.service"
indented.write_text('[Service]\n   User=root   \n', encoding="utf-8")
check("User=root with surrounding whitespace",
      lambda: mod.validate_unit_identity(indented.read_text()), True)

override = SCRATCH / "override.service"
override.write_text('[Service]\nUser=root\nUser=twilight-blog\n', encoding="utf-8")
check("later User= overrides an earlier root (systemd semantics: accepted)",
      lambda: mod.validate_unit_identity(override.read_text()), False)

print("\n=== validate_unit_source (POSIX-only permission checks) ===")
if not POSIX:
    print("  SKIP  file mode / parent dir mode assertions require POSIX semantics")
    skipped.append("posix-mode-checks")
else:
    good = SCRATCH / "good.service"
    good.write_text('[Service]\nUser=twilight-blog\nGroup=twilight-blog\n'
                    'ExecStart=/usr/local/bin/twilight-blog\n', encoding="utf-8")
    os.chmod(good, 0o644)
    check("well-formed unit file", lambda: mod.validate_unit_source(good, good.read_text()), False)

    world = SCRATCH / "world.service"
    world.write_text('[Service]\nUser=twilight-blog\n', encoding="utf-8")
    os.chmod(world, 0o666)
    check("group/world-writable file", lambda: mod.validate_unit_source(world, world.read_text()), True)

    shared = SCRATCH / "shared"
    shared.mkdir(exist_ok=True)
    inner = shared / "inner.service"
    inner.write_text('[Service]\nUser=twilight-blog\n', encoding="utf-8")
    os.chmod(inner, 0o600)
    os.chmod(shared, 0o777)
    check("world-writable parent directory (TOCTOU)",
          lambda: mod.validate_unit_source(inner, inner.read_text()), True)
    os.chmod(shared, 0o700)

print("\n=== the repository's own unit file ===")
real = HERE / "twilight-blog.service"
real_text = real.read_text(encoding="utf-8")
print(f"  User={mod.unit_directive(real_text, 'User')!r} "
      f"Group={mod.unit_directive(real_text, 'Group')!r}")
if POSIX:
    check("deploy/twilight-blog.service accepted",
          lambda: mod.validate_unit_source(real, real_text), False)
else:
    # 在 Windows 上只能验证内容层面的判定（User/Group），模式检查已跳过。
    user_ok = mod.unit_directive(real_text, "User") == mod.SERVICE_USER
    print(f"  {'PASS' if user_ok else 'FAIL'}  User= is the service account")
    if not user_ok:
        failures.append("real-unit-user")

for path in sorted(SCRATCH.glob("*")):
    if path.is_file():
        path.unlink()

print()
if skipped:
    print(f"SKIPPED (must be re-run on Linux): {skipped}")
if failures:
    print(f"FAILED: {failures}")
    sys.exit(1)
print("ALL CHECKS PASSED")
