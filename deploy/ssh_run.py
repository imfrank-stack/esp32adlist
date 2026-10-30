# -*- coding: utf-8 -*-
"""SSH 工具：执行远程命令 / SFTP 上传。凭据从环境变量或参数读取。"""
import os
import sys
import stat
import time
import paramiko

HOST = os.environ.get("DEPLOY_HOST", "124.70.59.229")
USER = os.environ.get("DEPLOY_USER", "root")
PASS = os.environ.get("DEPLOY_PASS", "")


def connect():
    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    c.connect(HOST, 22, USER, PASS, timeout=20, banner_timeout=20,
              allow_agent=False, look_for_keys=False)
    return c


def run(c, cmd, timeout=120):
    _, out, err = c.exec_command(cmd, timeout=timeout)
    o = out.read().decode("utf-8", "replace")
    e = err.read().decode("utf-8", "replace")
    rc = out.channel.recv_exit_status()
    return rc, o, e


def upload(c, local, remote):
    sftp = c.open_sftp()
    try:
        sftp.put(local, remote)
    finally:
        sftp.close()


if __name__ == "__main__":
    if not PASS:
        sys.exit("缺少 DEPLOY_PASS 环境变量")
    c = connect()
    try:
        for cmd in sys.argv[1:] or ["uname -a", "cat /etc/os-release | head -3", "free -m | head -2", "df -h / | tail -1"]:
            rc, o, e = run(c, cmd)
            print(f"$ {cmd}\n{ o}{e}(exit {rc})")
    finally:
        c.close()
