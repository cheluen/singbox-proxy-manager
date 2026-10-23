#!/usr/bin/env python3
"""统一发行文件系统回归；可额外传入旧管理程序及其内核验证原地升级。"""

import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Target(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"unified-release-proxy-ok"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


def main():
    if len(sys.argv) not in (2, 4):
        raise SystemExit("用法: test-release.py /新发行文件 [/旧管理程序 /旧内核]")
    binary = Path(sys.argv[1]).resolve(strict=True)
    expected_version = (Path(__file__).resolve().parent.parent / "internal" / "version" / "version.txt").read_text().strip()
    curl = shutil.which("curl")
    assert curl, "需要 curl 验证 HTTP 和 SOCKS5 链路"
    with tempfile.TemporaryDirectory(prefix="sbpm-release-") as temp:
        root = Path(temp)
        custom_config = os.environ.get("SBPM_RELEASE_TEST_CUSTOM_CONFIG") == "1"
        config = root / ("persistent-data" if custom_config else "config")
        installed = root / "manager"
        port = free_port()
        base = f"http://127.0.0.1:{port}"
        # 不继承调用环境中的数据库、代理或开发配置，测试绝不接触远程数据。
        env = {"PATH": str(root), "PORT": str(port), "BIND_ADDRESS": "127.0.0.1",
               "TZ": "UTC", "SBPM_UPDATE_CHECK_DISABLED": "1",
               "SBPM_SINGBOX_LOG_OUTPUT": "stdout",
               "ADMIN_PASSWORD": "release-initial-password"}
        if custom_config:
            env["CONFIG_DIR"] = str(config)
        token = ""
        child = None
        log = (root / "manager.log").open("w+")
        target = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Target)
        thread = threading.Thread(target=target.serve_forever, daemon=True)
        thread.start()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def request(route, data=None, method=None):
            headers = {"Authorization": f"Bearer {token}"}
            if data is not None:
                headers["Content-Type"] = "application/json"
            req = urllib.request.Request(base + route, headers=headers, method=method,
                                         data=None if data is None else json.dumps(data).encode())
            with opener.open(req, timeout=10) as response:
                body = response.read()
                return json.loads(body) if route.startswith("/api/") else body

        def stop():
            nonlocal child
            if child is not None:
                child.terminate()
                try:
                    child.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()
                    raise AssertionError("管理程序未能正常停止")
                child = None

        def start(source=None):
            nonlocal child
            if source:
                shutil.copy2(source, installed)
                installed.chmod(0o700)
            child = subprocess.Popen([str(installed)], cwd=root, env=env, stdout=log, stderr=log)
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                assert child.poll() is None, "管理程序提前退出"
                try:
                    request("/api/version")
                    return
                except (urllib.error.URLError, TimeoutError):
                    time.sleep(0.1)
            raise AssertionError("启动超时")

        def login(password):
            nonlocal token
            token = request("/api/login", {"password": password})["token"]

        def proxy(node):
            for flag in ("--proxy", "--socks5-hostname"):
                address = f"127.0.0.1:{node['inbound_port']}"
                if flag == "--proxy":
                    address = "http://" + address
                result = subprocess.run(
                    [curl, "-fsS", "--max-time", "10", "--noproxy", "", flag, address,
                     "--proxy-user", f"{node['username']}:{node['password']}",
                     f"http://127.0.0.1:{target.server_port}/"],
                    capture_output=True, env={"PATH": "/usr/bin:/bin"})
                assert result.returncode == 0, result.stderr.decode()
                assert result.stdout == b"unified-release-proxy-ok", "代理链路响应错误"

        def kernel_path():
            status = request("/api/runtime/status")
            assert status["running"], status
            path = Path(os.readlink(f"/proc/{status['pid']}/exe"))
            assert path.parent == config / "kernels", path
            assert path.name == "sing-box-" + hashlib.sha256(path.read_bytes()).hexdigest()
            assert path.stat().st_mode & 0o777 == 0o700
            return path

        try:
            legacy = len(sys.argv) == 4
            if legacy:
                env["SINGBOX_BINARY"] = str(Path(sys.argv[3]).resolve(strict=True))
            start(Path(sys.argv[2]).resolve(strict=True) if legacy else binary)
            previous_version = request("/api/version")["version"]
            login(env["ADMIN_PASSWORD"])
            request("/api/settings", {"start_port": free_port(), "preserve_inbound_ports": True}, "PUT")
            node = request("/api/nodes", {
                "name": "release-persistent-direct", "remark": "upgrade-preserves-data",
                "type": "direct", "config": "{}", "username": "release-proxy-user",
                "password": "release-proxy-password", "enabled": True,
                "tcp_reuse_enabled": True})
            proxy(node)
            request("/api/settings", {"admin_password": "release-retained-password"}, "PUT")
            env.pop("ADMIN_PASSWORD")
            login("release-retained-password")
            before_nodes = request("/api/nodes")
            before_settings = request("/api/settings")
            dbpath = config / "proxy.db"
            assert dbpath.exists()
            inode = dbpath.stat().st_ino
            stop()

            # 同时放置历史环境变量、PATH 内核、同目录内核和旧前端。
            stale = root / "sing-box"
            stale.write_text("#!/bin/sh\necho stale-kernel-used >&2\nexit 91\n")
            stale.chmod(0o700)
            shutil.copy2(stale, config / "sing-box")
            env["SINGBOX_BINARY"] = str(stale)
            dist = root / "frontend" / "dist"
            dist.mkdir(parents=True)
            (dist / "index.html").write_text("stale-frontend-must-not-be-served")
            start(binary)
            assert request("/api/version")["version"] == expected_version, "发行文件版本不匹配"
            login("release-retained-password")
            assert request("/api/nodes") == before_nodes, "升级改变了节点数据"
            assert request("/api/settings") == before_settings, "升级改变了设置"
            assert dbpath.stat().st_ino == inode, "升级替换了数据库文件"
            html = request("/")
            assert b"stale-frontend" not in html and b"<html" in html
            current = kernel_path()
            original_hash = hashlib.sha256(current.read_bytes()).hexdigest()
            proxy(node)
            stop()

            # 内核损坏和执行权限丢失均自动恢复；不能回退旧内核。
            for damage in ("content", "mode", "missing"):
                if damage == "content":
                    current.write_bytes(b"corrupted-kernel")
                elif damage == "mode":
                    current.chmod(0o600)
                else:
                    current.unlink()
                start()
                login("release-retained-password")
                assert kernel_path() == current
                assert hashlib.sha256(current.read_bytes()).hexdigest() == original_hash
                assert request("/api/nodes") == before_nodes
                proxy(node)
                stop()

            start()
            login("release-retained-password")
            status = request("/api/runtime/status")
            old_pid = status["pid"]
            os.kill(old_pid, signal.SIGKILL)
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                status = request("/api/runtime/status")
                if status["running"] and status["pid"] != old_pid:
                    break
                time.sleep(0.2)
            assert status["running"] and status["pid"] != old_pid, "内核崩溃未自动恢复"
            kernel_path()
            proxy(node)
            request(f"/api/nodes/{node['id']}", method="DELETE")
            assert request("/api/nodes") == []
            stop()
            with sqlite3.connect(dbpath) as db:
                assert db.execute("PRAGMA integrity_check").fetchone() == ("ok",)
            assert not list((config / "kernels").glob(".sing-box-*"))
            log.flush()
            assert "stale-kernel-used" not in (root / "manager.log").read_text()
            print(json.dumps({"success": True, "legacy_upgrade": legacy,
                              "previous_version": previous_version, "version": expected_version,
                              "custom_config": custom_config,
                              "checks": ["single-file", "embedded-frontend", "sqlite-retention",
                                         "password-retention", "kernel-priority", "kernel-repair",
                                         "http-proxy", "socks5-proxy", "crash-recovery", "CRUD"]}))
        except Exception:
            log.flush()
            print((root / "manager.log").read_text()[-15000:], file=sys.stderr)
            raise
        finally:
            stop()
            target.shutdown()
            target.server_close()
            thread.join(timeout=5)
            log.close()


if __name__ == "__main__":
    main()
