#!/usr/bin/env python3
"""Opt-in read-only GitHub/source-capability smoke using a disposable application."""
import argparse
import json
import os
import platform
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--online', action='store_true', help='Explicitly allow metadata reads from the two fixed GitHub repositories')
    parser.add_argument('--node', required=True)
    parser.add_argument('--npm-cli', required=True)
    args = parser.parse_args()
    if not args.online:
        parser.error('Use --online to explicitly allow read-only GitHub metadata access; no source install is performed')
    native = 'windows-amd64' if os.name == 'nt' else 'linux-' + ('arm64' if platform.machine() in ['aarch64', 'arm64'] else 'amd64')
    packaged = ROOT / 'dist' / (native + '-portable')
    name = 'cliproxyapi-plus.exe' if os.name == 'nt' else 'cliproxyapi-plus'
    with tempfile.TemporaryDirectory(prefix='plus-source-status-') as temporary:
        work = Path(temporary)
        binary = work / name
        shutil.copy2(packaged / name, binary)
        config, data = work / 'config.yaml', work / 'data'
        tools_file = work / 'update-tools.json'
        go_root = Path(subprocess.check_output(['go', 'env', 'GOROOT'], cwd=ROOT, text=True).strip())
        go_binary = go_root / 'bin' / ('go.exe' if os.name == 'nt' else 'go')
        tools_file.write_text(json.dumps({'python': sys.executable, 'go': str(go_binary), 'node': str(Path(args.node).resolve()), 'npm_cli': str(Path(args.npm_cli).resolve())}), encoding='utf-8')
        with socket.socket() as reserved:
            reserved.bind(('127.0.0.1', 0))
            port = reserved.getsockname()[1]
        address = '127.0.0.1:' + str(port)
        base = 'http://' + address
        env = {k: v for k, v in os.environ.items() if k.upper() in ['PATH', 'SYSTEMROOT', 'WINDIR', 'TEMP', 'TMP', 'USERPROFILE', 'LOCALAPPDATA', 'APPDATA', 'HOME', 'PATHEXT']}
        env['WRITABLE_PATH'] = str(work)
        hidden = {'creationflags': subprocess.CREATE_NO_WINDOW} if os.name == 'nt' else {}
        subprocess.run([str(binary), '--init', '--config', str(config), '--data-dir', str(data), '--listen', address], env=env, cwd=work, capture_output=True, check=True, **hidden)
        admin = (data / 'admin.key').read_text().strip()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        def request(path, key='', method='GET', body=None):
            headers = {'Authorization': 'Bearer ' + key} if key else {}
            raw = None
            if body is not None:
                raw = json.dumps(body).encode()
                headers.update({'Content-Type': 'application/json', 'Origin': base})
            req = urllib.request.Request(base + path, data=raw, method=method, headers=headers)
            try:
                with opener.open(req, timeout=90) as response:
                    return response.status, json.load(response)
            except urllib.error.HTTPError as error:
                return error.code, error.read().decode(errors='replace')
        with (work / 'console.log').open('wb') as console:
            process = subprocess.Popen([str(binary), '--config', str(config), '--data-dir', str(data), '--web-updates', '--source-updates', '--source-workspace', str(packaged / 'updater-source'), '--source-tools', str(tools_file)], env=env, cwd=work, stdout=console, stderr=console, **hidden)
            try:
                deadline = time.monotonic() + 90
                while True:
                    if process.poll() is not None:
                        raise RuntimeError('Disposable application exited before readiness')
                    try:
                        code, _ = request('/plus-api/ready')
                        if code == 200:
                            break
                    except OSError:
                        pass
                    if time.monotonic() >= deadline:
                        raise TimeoutError('Readiness timed out')
                    time.sleep(0.05)
                for key in ['', (data / 'api.key').read_text().strip(), (data / 'core-management.key').read_text().strip()]:
                    assert request('/plus-api/releases/upstreams', key)[0] == 401
                    assert request('/plus-api/releases/upstreams/install', key, 'POST', {'project': 'all'})[0] == 401
                while True:
                    code, status = request('/plus-api/releases/upstreams', admin)
                    assert code == 200
                    if all(not item['stale'] for item in status['projects']) and status['enabled']:
                        break
                    if time.monotonic() >= deadline:
                        raise RuntimeError('Direct metadata/tool status did not become ready: ' + json.dumps(status))
                    time.sleep(0.1)
                assert status['mode'] == 'direct-upstream-source' and status['tools']['ready']
                assert {item['repository'] for item in status['projects']} == {'router-for-me/CLIProxyAPI', 'seakee/CPA-Manager-Plus'}
                code, releases = request('/plus-api/releases', admin)
                assert code == 200 and not releases['configured']
                code, operations = request('/plus-api/releases/operations', admin)
                assert code == 200 and operations['enabled'] and not operations['busy'] and not operations['history']
                assert not Path(str(data) + '.maintenance').exists()
                identity = json.loads(subprocess.check_output([str(binary), '--version'], **hidden))
                report = {'passed': True, 'version': identity['version'], 'product_source_sha256': identity['product_source_sha256'], 'upstream_lock_sha256': identity['upstream_lock_sha256'], 'target': native, 'projects': status['projects'], 'tools': status['tools'], 'checks': ['no release index or release server', 'real fixed-repository metadata', 'both project identities separately reported', 'actual local build-tool probes', 'packaged source header matches running binary', 'stored-admin credential boundary', 'no source install requested', 'current user service/data untouched']}
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
        (ROOT / '.build/source-updates-status-smoke-report.json').write_text(json.dumps(report, indent=2) + '\n', encoding='utf-8')
        print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
