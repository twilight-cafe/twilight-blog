#!/usr/bin/env python3
# -*- coding: utf-8 -*-

import argparse
import os
import sys
import subprocess
import pwd

def check_root():
    if os.geteuid() != 0:
        print("错误: 必须使用 root 权限运行此脚本 (请使用 sudo)。")
        sys.exit(1)

def check_user(username):
    try:
        pwd.getpwnam(username)
        return True
    except KeyError:
        print(f"错误: 系统上不存在用户 '{username}'。")
        sys.exit(1)

def check_program_dir(program_dir):
    if not os.path.isdir(program_dir):
        print(f"错误: 程序目录 '{program_dir}' 不存在或不是一个目录。")
        sys.exit(1)
    
    pmail_bin = os.path.join(program_dir, "pmail")
    if not os.path.isfile(pmail_bin):
        print(f"警告: 在 '{program_dir}' 中未找到可执行文件 'pmail'。")
        print("请确保 PMail 二进制文件已正确下载并命名为 'pmail'。")
    else:
        # 确保可执行权限
        current_mode = os.stat(pmail_bin).st_mode
        if not (current_mode & 0o111):
            print(f"正在为 {pmail_bin} 添加可执行权限...")
            os.chmod(pmail_bin, 0o755)

def main():
    parser = argparse.ArgumentParser(description="在 Ubuntu 上将 PMail 安装为 systemd 服务。")
    parser.add_argument("username", help="运行 PMail 服务的系统用户名 (例如: pmail_user 或 root)。")
    parser.add_argument("program_dir", help="PMail 程序所在的绝对路径目录。")
    
    args = parser.parse_args()
    
    check_root()
    check_user(args.username)
    check_program_dir(args.program_dir)
    
    # 转换为绝对路径以防万一
    program_dir_abs = os.path.abspath(args.program_dir)
    pmail_bin_abs = os.path.join(program_dir_abs, "pmail")
    
    service_name = "pmail"
    service_file_path = f"/etc/systemd/system/{service_name}.service"
    
    # 只有非 root 用户才需要赋予绑定特权端口(25, 80, 443等)的能力
    caps_config = ""
    if args.username != "root":
        caps_config = """# 允许非 root 用户绑定到特权端口 (25, 80, 443, 110, 465, 587, 993, 995)
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
"""

    # 构建 systemd 服务文件内容
    service_content = f"""[Unit]
Description=PMail Server
After=network.target

[Service]
Type=simple
User={args.username}
WorkingDirectory="{program_dir_abs}"
ExecStart="{pmail_bin_abs}"
Restart=on-failure
LimitNOFILE=65536
{caps_config}
[Install]
WantedBy=multi-user.target
"""

    try:
        with open(service_file_path, 'w') as f:
            f.write(service_content)
        print(f"成功创建服务文件: {service_file_path}")
        os.chmod(service_file_path, 0o644)
    except IOError as e:
        print(f"写入服务文件失败: {e}")
        sys.exit(1)
        
    print("正在重载 systemd daemon...")
    subprocess.run(["systemctl", "daemon-reload"], check=True)
    
    print(f"正在设置 {service_name} 服务开机自启...")
    subprocess.run(["systemctl", "enable", f"{service_name}.service"], check=True)
    
    print(f"正在启动 {service_name} 服务...")
    result = subprocess.run(["systemctl", "start", f"{service_name}.service"])
    if result.returncode != 0:
        print(f"警告: 启动 {service_name} 服务可能失败，请检查日志。")
        
    print("\n正在检查服务状态...")
    subprocess.run(["systemctl", "status", f"{service_name}.service", "--no-pager"])
    
    print("\n" + "="*60)
    print("PMail 服务安装完成!")
    print(f"查看实时日志: sudo journalctl -u {service_name}.service -f")
    print(f"停止服务:     sudo systemctl stop {service_name}")
    print(f"重启服务:     sudo systemctl restart {service_name}")
    print("="*60)
    
    print("\n提示: PMail 默认使用 80 端口进行初始化设置，请确保该端口未被占用。")
    print(f"注意: 请确保用户 '{args.username}' 对目录 '{program_dir_abs}' 具有读写权限，否则 PMail 将无法写入配置文件。")
    print(f"如需修改权限，可执行: sudo chown -R {args.username}:{args.username} {program_dir_abs}")

if __name__ == "__main__":
    main()