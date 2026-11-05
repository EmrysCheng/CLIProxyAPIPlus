#!/usr/bin/env python3
"""Smoke-test a copied standalone release-server bundle without runtime interpreters."""
import argparse
import hashlib
import json
import os
import platform
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bundle', type=Path)
    args = parser.parse_args()
    target = 'windows-amd64' if os.name == 'nt' else 'linux-' + ('arm64' if platform.machine() in ['aarch64', 'arm64'] else 'amd64')
    original = args.bundle or ROOT / 'dist' / ('release-server-' + target + '-portable')
    with tempfile.TemporaryDirectory(prefix='plus-release-server-') as temporary:
        work = Path(temporary)
        copied = work / 'bundle'
        shutil.copytree(original, copied)
        outside = work / 'different-cwd'
        outside.mkdir()
        binary = copied / ('cliproxyapi-plus-release-server.exe' if os.name == 'nt' else 'cliproxyapi-plus-release-server')
        with socket.socket() as reservation:
            reservation.bind(('127.0.0.1', 0))
            port = reservation.getsockname()[1]
        base = 'http://127.0.0.1:' + str(port)
        config_file = copied / 'release-server.json'
        config = json.loads(config_file.read_text(encoding='utf-8'))
        config['listen'] = '127.0.0.1:' + str(port)
        config_file.write_text(json.dumps(config), encoding='utf-8')
        index_file = copied / 'releases/index.json'
        index = json.loads(index_file.read_text(encoding='utf-8'))
        for asset in index['release']['assets']:
            asset['url'] = base + '/' + asset['name']
        index_file.write_text(json.dumps(index), encoding='utf-8')
        # Service has no interpreter/toolchain available in PATH. The test runner
        # itself is Python, but the copied server never starts Python/Node/Go.
        env = {k: v for k, v in os.environ.items() if k.upper() in ['SYSTEMROOT', 'WINDIR']}
        env.update(PATH='', TEMP=str(work), TMP=str(work), TMPDIR=str(work))
        hidden = {'creationflags': subprocess.CREATE_NO_WINDOW} if os.name == 'nt' else {}
        identity = json.loads(subprocess.check_output([str(binary), '--version'], env=env, cwd=outside, **hidden))
        subprocess.run([str(binary), '--check'], env=env, cwd=outside, check=True, capture_output=True, **hidden)
        console = work / 'console.log'
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with console.open('wb') as output:
            process = subprocess.Popen([str(binary)], env=env, cwd=outside, stdout=output, stderr=output, **hidden)
            try:
                deadline = time.monotonic() + 30
                while True:
                    if process.poll() is not None:
                        raise RuntimeError('Copied server exited: ' + console.read_text(errors='replace'))
                    try:
                        with opener.open(base + '/healthz', timeout=1) as response:
                            if response.status == 200:
                                break
                    except (OSError, urllib.error.HTTPError):
                        pass
                    if time.monotonic() >= deadline:
                        raise TimeoutError('Standalone server did not become ready')
                    time.sleep(0.05)
                with opener.open(base + '/index.json', timeout=5) as response:
                    served = json.load(response)
                assert served == index
                downloads = []
                for asset in index['release']['assets']:
                    request = urllib.request.Request(asset['url'], headers={'Range': 'bytes=0-7'})
                    with opener.open(request, timeout=5) as response:
                        assert response.status == 206 and len(response.read()) == 8
                    request = urllib.request.Request(asset['url'], method='HEAD')
                    with opener.open(request, timeout=5) as response:
                        assert response.status == 200 and response.read() == b''
                    checksum = hashlib.sha256()
                    with opener.open(asset['url'], timeout=30) as response:
                        while block := response.read(1024 * 1024):
                            checksum.update(block)
                    assert checksum.hexdigest() == asset['sha256']
                    downloads.append(asset['name'])
                for path, method, host, expected in [('/', 'GET', None, 404), ('/release-server.json', 'GET', None, 404), ('/logs/release-server.log', 'GET', None, 404), ('/index.json', 'POST', None, 405), ('/index.json', 'GET', 'attacker.example', 403)]:
                    request = urllib.request.Request(base + path, method=method, headers={'Host': host} if host else {})
                    try:
                        opener.open(request, timeout=5)
                        raise AssertionError('Request unexpectedly allowed: ' + path)
                    except urllib.error.HTTPError as error:
                        assert error.code == expected
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
        assert console.stat().st_size < 4096, 'Access logs should not flood the console'
        log = copied / 'logs/release-server.log'
        assert log.is_file() and 'status=206' in log.read_text(encoding='utf-8')
        report = {'passed': True, 'version': identity['version'], 'product_source_sha256': identity['product_source_sha256'], 'target': target, 'bundled_assets_downloaded': downloads, 'checks': ['standalone executable with empty PATH', 'config relative to executable, not working directory', 'startup package SHA256 verification', 'index and every package download', 'HEAD and Range support', 'no directory/config/log exposure', 'read-only methods and literal Host boundary', 'access logs go to rotating file, not console'], 'user_service_restarted': False}
        destination = ROOT / '.build/release-server-smoke-report.json'
        destination.write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
        print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
