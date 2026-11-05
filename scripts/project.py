#!/usr/bin/env python3
"""Reproducible product preparation/build tools. No upstream code is run during prepare."""
from __future__ import annotations
import datetime
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import urllib.request
import uuid
import re
import platform
import tarfile
import zipfile
from build_progress import step

ROOT = Path(__file__).resolve().parents[1]
BUILD = ROOT / '.build'
VERSION = (ROOT / 'VERSION').read_text(encoding='utf-8').strip()
if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?', VERSION):
    raise ValueError('Invalid product version')


def tree_digest(path: Path) -> str:
    digest = hashlib.sha256()
    for item in sorted(path.rglob('*'), key=lambda p: p.relative_to(path).as_posix().encode('utf-8')):
        if item.is_symlink():
            raise ValueError(f'Symlinks are not permitted in a source snapshot: {item}')
        if item.is_file():
            name = item.relative_to(path).as_posix().encode()
            data = item.read_bytes()
            digest.update(len(name).to_bytes(4, 'big'))
            digest.update(name)
            digest.update(len(data).to_bytes(8, 'big'))
            digest.update(data)
    return digest.hexdigest()


def child(root: Path, name: str) -> Path:
    path = (root / name).resolve()
    if not path.is_relative_to(root.resolve()) or path == root.resolve():
        raise ValueError(f'Path escapes its root: {name}')
    return path


def apply_changes(target: Path, changes: list[dict]) -> list[Path]:
    staged = []
    for change in changes:
        path = child(target, change['path'])
        raw = path.read_bytes()
        # Known upstream revisions may share the exact same patch operations.
        # Accept only explicitly reviewed full-file digests, never fuzzy context.
        preimages = change['sha256']
        if isinstance(preimages, str):
            preimages = [preimages]
        if (not isinstance(preimages, list) or not preimages
                or any(not isinstance(pin, str) or not re.fullmatch(r'[0-9a-f]{64}', pin)
                       for pin in preimages)
                or len(set(preimages)) != len(preimages)):
            raise ValueError(f'Invalid patch preimage allowlist: {change["path"]}')
        if hashlib.sha256(raw).hexdigest() not in preimages:
            raise ValueError(f'Patch preimage drift: {change["path"]}')
        text = raw.decode('utf-8')
        for operation in change['operations']:
            found = text.count(operation['find'])
            if found != operation['count']:
                raise ValueError(f'Patch context count mismatch: {path}: {found}')
            text = text.replace(operation['find'], operation['replace'])
        staged.append((path, text))
    # Validate every preimage before writing any patch output.
    for path, text in staged:
        path.write_text(text, encoding='utf-8', newline='\n')
    return [path for path, _ in staged]


def lockfile() -> dict:
    lock = json.loads((ROOT / 'upstream.lock.json').read_text(encoding='utf-8'))
    if lock.get('schema_version') != 1:
        raise ValueError('Unsupported upstream lock schema')
    if set(lock['projects']) != {'cliproxyapi', 'cpa-manager-plus'}:
        raise ValueError('Both official upstreams must be locked')
    expected = {'cliproxyapi': 'router-for-me/CLIProxyAPI', 'cpa-manager-plus': 'seakee/CPA-Manager-Plus'}
    for name, project in lock['projects'].items():
        if project['repository'] != expected[name] or not re.fullmatch('[0-9a-f]{40}', project['commit']):
            raise ValueError(f'Unapproved upstream identity: {name}')
    return lock


def source_inventory(root: Path) -> dict:
    result = {}
    ignored_assets = {'apps/manager-server/internal/httpapi/web/management.html', 'apps/manager-server/internal/httpapi/web/favicon.ico', 'apps/manager-server/internal/httpapi/web/apple-touch-icon.png'}
    for path in root.rglob('*'):
        relative = path.relative_to(root).as_posix()
        if any(part in {'node_modules', 'dist', 'dist-demo', '__pycache__'} for part in path.relative_to(root).parts):
            continue
        if relative in ignored_assets or path.name.startswith('.plus-'):
            continue
        if path.is_symlink():
            raise ValueError('Prepared sources may not contain symlinks')
        if path.is_file():
            result[relative] = hashlib.sha256(path.read_bytes()).hexdigest()
    return result


def isolate_artifacts():
    # Go's ./... ignores .build but NOT dist. SDK compiler test fixtures are
    # distribution data, not product packages: retain them behind a module
    # boundary instead of accidentally running upstream compiler torture tests.
    directory = ROOT / 'dist'
    directory.mkdir(parents=True, exist_ok=True)
    marker = directory / 'go.mod'
    expected = 'module cliproxyapi-plus-build-artifacts\n\ngo 1.26.10\n'
    if marker.exists() and marker.read_text(encoding='utf-8') != expected:
        raise ValueError('Unexpected artifact module boundary; review dist/go.mod')
    if not marker.exists():
        marker.write_text(expected, encoding='utf-8', newline='\n')


def prepare() -> dict[str, Path]:
    isolate_artifacts()
    lock = lockfile()
    prepared = {}
    source_root = BUILD / 'sources'
    source_root.mkdir(parents=True, exist_ok=True)
    for name, project in lock['projects'].items():
        source = ROOT / 'upstream' / name
        actual = tree_digest(source)
        if actual != project['tree_sha256']:
            raise ValueError(f'Immutable source snapshot changed: {name}')
        patch_dir = ROOT / 'patches' / name
        patch_hash = tree_digest(patch_dir) if patch_dir.exists() else 'none'
        fingerprint = hashlib.sha256(('layout-v3:' + actual + ':' + patch_hash).encode()).hexdigest()
        target = source_root / f'{name}-{fingerprint[:16]}'
        marker = target / '.plus-prepared.json'
        if marker.exists():
            record = json.loads(marker.read_text(encoding='utf-8'))
            if record['fingerprint'] != fingerprint or source_inventory(target) != record['inventory']:
                raise ValueError('Prepared-source cache drift; use a fresh .build or rebuild from the locked sources')
        else:
            if target.exists():
                raise ValueError(f'Incomplete prepared directory: {target}')
            # Temporary output is confined to .build; the stable workspace is published last.
            temp = source_root / (name + '-preparing-' + uuid.uuid4().hex)
            temp.mkdir()
            shutil.copytree(source, temp, dirs_exist_ok=True)
            changed = []
            manifest = patch_dir / 'changes.json'
            if manifest.exists():
                changed.extend(apply_changes(temp, json.loads(manifest.read_text(encoding='utf-8'))))
            overlays = patch_dir / 'files'
            if overlays.exists():
                for overlay in sorted(overlays.rglob('*')):
                    if overlay.is_file():
                        destination = child(temp, overlay.relative_to(overlays).as_posix())
                        if destination.exists():
                            raise ValueError(f'Overlay would overwrite existing source: {destination}')
                        destination.parent.mkdir(parents=True, exist_ok=True)
                        shutil.copyfile(overlay, destination)
                        changed.append(destination)
            go_files = [str(p) for p in changed if p.suffix == '.go']
            if go_files:
                subprocess.run(['gofmt', '-w', *go_files], check=True)
            (temp / '.plus-prepared.json').write_text(json.dumps({'fingerprint': fingerprint, 'inventory': source_inventory(temp)}), encoding='utf-8')
            temp.rename(target)
        prepared[name] = target
        print(f'Prepared {name}: {project["ref"]} @ {project["commit"][:12]}', flush=True)
    use = ['.'] + [prepared['cliproxyapi'].relative_to(ROOT).as_posix(),
                   (prepared['cpa-manager-plus'] / 'apps/manager-server').relative_to(ROOT).as_posix()]
    workspace = 'go 1.26.10\n\nuse (\n' + ''.join(f' ./{p}\n' for p in use) + ')\n'
    (BUILD / 'go.work.pending').write_text(workspace, encoding='utf-8')
    os.replace(BUILD / 'go.work.pending', ROOT / 'go.work')
    (BUILD / 'prepared.json').write_text(json.dumps({name: str(path) for name, path in prepared.items()}, indent=2), encoding='utf-8')
    return prepared


def run(command: list[str], cwd: Path = ROOT, env: dict | None = None):
    print('>', ' '.join(map(str, command)), flush=True)
    subprocess.run(command, cwd=cwd, env=env, check=True)


def environment(args) -> dict:
    env = os.environ.copy()
    env['GOWORK'] = str(ROOT / 'go.work')
    env['GOCACHE'] = os.environ.get('CLIPROXY_PLUS_GOCACHE', str(BUILD / 'go-cache'))
    env.setdefault('GOFLAGS', '-p=4')
    env.setdefault('GOMAXPROCS', '4')
    env['CGO_ENABLED'] = '1' if getattr(args, 'variant', 'portable') == 'full' else '0'
    env.pop('GOOS', None)
    env.pop('GOARCH', None)
    if args.node:
        env['PATH'] = str(Path(args.node).resolve().parent) + os.pathsep + env.get('PATH', '')
    env['npm_config_cache'] = str(BUILD / 'npm-cache')
    env['VERSION'] = VERSION
    env['VITE_CPA_PLUS_EMBEDDED'] = '1'
    return env


def npm_command(args) -> list[str]:
    if args.npm_cli:
        return [args.node or 'node', str(Path(args.npm_cli).resolve())]
    executable = shutil.which('npm')
    if not executable:
        raise ValueError('npm is required; pass --node and --npm-cli if not on PATH')
    return [executable]


def build_web(args, prepared, env):
    env = env.copy()
    # __APP_VERSION__ is the upstream Manager version used by inherited links.
    # The product page reads its independent version from /plus-api/version.
    env['VERSION'] = lockfile()['projects']['cpa-manager-plus']['ref']
    manager = prepared['cpa-manager-plus']
    lock = lockfile()
    changes = json.loads((ROOT / 'patches/cpa-manager-plus/changes.json').read_text(encoding='utf-8'))
    front_changes = [item for item in changes if item['path'].startswith('apps/web/')]
    overlay = ROOT / 'patches/cpa-manager-plus/files/apps/web'
    key = hashlib.sha256(('frontend-v2:' + lock['projects']['cpa-manager-plus']['tree_sha256'] + json.dumps(front_changes, sort_keys=True) + tree_digest(overlay) + VERSION).encode()).hexdigest()[:20]
    work = BUILD / 'frontend' / key
    if not work.exists():
        work.mkdir(parents=True)
        for name in ['package.json', 'package-lock.json']:
            shutil.copyfile(manager / name, work / name)
        shutil.copytree(manager / 'apps/web', work / 'apps/web')
        # Web tests import repository-level shared provider fixtures.
        if (manager / 'tests').exists():
            shutil.copytree(manager / 'tests', work / 'tests')
        docs_package = manager / 'apps/docs/package.json'
        if docs_package.exists():
            (work / 'apps/docs').mkdir(parents=True)
            shutil.copyfile(docs_package, work / 'apps/docs/package.json')
    for name in ['eslint.config.js', '.prettierrc']:
        if (manager / name).exists():
            shutil.copyfile(manager / name, work / name)
    npm = npm_command(args)
    install_stamp = work / '.plus-npm-install.json'
    lock_hash = hashlib.sha256((work / 'package-lock.json').read_bytes()).hexdigest()
    if not install_stamp.exists() or json.loads(install_stamp.read_text(encoding='utf-8')).get('lock') != lock_hash:
        step('frontend_dependencies')
        run(npm + ['ci', '--ignore-scripts', '--no-audit', '--no-fund'], work, env)
        install_stamp.write_text(json.dumps({'lock': lock_hash}), encoding='utf-8')
    step('frontend_build')
    run(npm + ['--workspace', 'apps/web', 'run', 'build'], work, env)
    web = work / 'apps/web/dist'
    destination = manager / 'apps/manager-server/internal/httpapi/web'
    html = (web / 'index.html').read_bytes()
    if len(html) < 100_000:
        raise ValueError('Refusing to embed a source placeholder')
    for source, target in [('index.html', 'management.html'), ('favicon.ico', 'favicon.ico'), ('apple-touch-icon.png', 'apple-touch-icon.png')]:
        shutil.copyfile(web / source, destination / target)
    stamp = {'sha256': hashlib.sha256(html).hexdigest(), 'input_key': key, 'work': str(work), 'version': VERSION, 'toolchain': {'node': subprocess.check_output([args.node or 'node', '--version'], env=env, text=True).strip(), 'npm': subprocess.check_output(npm + ['--version'], cwd=work, env=env, text=True).strip()}}
    (manager / '.plus-web.json').write_text(json.dumps(stamp), encoding='utf-8')
    (BUILD / 'frontend-current.json').write_text(json.dumps(stamp), encoding='utf-8')


def validate_web(prepared):
    manager = prepared['cpa-manager-plus']
    stamp = manager / '.plus-web.json'
    html = (manager / 'apps/manager-server/internal/httpapi/web/management.html').read_bytes()
    if not stamp.exists() or hashlib.sha256(html).hexdigest() != json.loads(stamp.read_text(encoding='utf-8'))['sha256']:
        raise ValueError('No verified frontend build; run build-web first or omit --skip-web')


def write_release_tar(archive: Path, directory: Path, binary_name: str, executable_names=()):
    # Windows stat does not retain POSIX executable bits for a Linux cross-build.
    with tarfile.open(archive, 'w:gz') as out:
        for file in sorted(directory.rglob('*'), key=lambda p: p.relative_to(directory).as_posix()):
            if file.is_symlink():raise ValueError('Release packages cannot contain symlinks')
            if file.is_file():
                name=file.relative_to(directory).as_posix()
                info=out.gettarinfo(str(file),arcname=name)
                info.mode=0o755 if name==binary_name or name in executable_names else 0o644
                info.uid=info.gid=0;info.uname=info.gname=''
                with file.open('rb') as stream:out.addfile(info,stream)


def check_updates(channel: str):
    lock = lockfile()
    for name, project in lock['projects'].items():
        base = 'https://api.github.com/repos/' + project['repository']
        def get(path):
            request = urllib.request.Request(base + path, headers={'User-Agent': 'CLIProxyAPIPlus', 'Accept': 'application/vnd.github+json'})
            with urllib.request.urlopen(request, timeout=30) as response:
                return json.load(response)
        ref = get('/releases/latest')['tag_name'] if channel == 'stable' else 'main'
        commit = get('/commits/' + ref)['sha']
        print(json.dumps({'project': name, 'channel': channel, 'ref': ref, 'commit': commit,
                          'locked': project['commit'], 'changed': commit != project['commit']}))
    print('Read-only check. Use upstreams.py stage/validate/promote for a verified source update; production installation remains explicit.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['prepare', 'verify', 'test', 'test-e2e', 'test-update', 'test-source', 'test-source-build', 'build-web', 'build', 'build-release-server', 'check-updates', 'test-web'])
    parser.add_argument('--go', default='go')
    parser.add_argument('--node')
    parser.add_argument('--npm-cli')
    parser.add_argument('--tool-bundle', help='Reuse a verified bundled SDK directory instead of downloading SDKs while packaging')
    parser.add_argument('--skip-web', action='store_true')
    parser.add_argument('--target', default='')
    parser.add_argument('--variant', choices=['portable', 'full'], default='portable')
    parser.add_argument('--skip-sbom', action='store_true')
    parser.add_argument('--release-listen', default='127.0.0.1:26973', help='Local release-server literal loopback IP and fixed port')
    parser.add_argument('--channel', choices=['stable', 'edge'], default='stable')
    args = parser.parse_args()
    if args.command == 'check-updates':
        check_updates(args.channel)
        return
    if args.command == 'build': step('preparing_source')
    prepared = prepare()
    if args.command in ['prepare', 'verify']:
        return
    env = environment(args)
    if args.command == 'build-release-server':
        run([sys.executable, '-m', 'unittest', 'discover', '-s', 'tests', '-p', 'test_*.py', '-v'])
        run([args.go, 'test', './...'], env=env)
        import release_server
        release_server.build(args, env)
        return
    if args.command == 'test-web':
        # Preserve upstream standalone coverage. Product tests explicitly opt into embedded mode.
        env['VITE_CPA_PLUS_EMBEDDED'] = '0'
        validate_web(prepared)
        front = json.loads((BUILD / 'frontend-current.json').read_text(encoding='utf-8'))
        for name in ['eslint.config.js', '.prettierrc']:
            shutil.copyfile(prepared['cpa-manager-plus'] / name, Path(front['work']) / name)
        run([args.node or 'node', str(ROOT / 'scripts/check_workflows.cjs'), front['work']], env=env)
        run(npm_command(args) + ['--workspace', 'apps/web', 'run', 'lint'], Path(front['work']), env)
        run(npm_command(args) + ['--workspace', 'apps/web', 'run', 'test', '--', 'src'], Path(front['work']), env)
        return
    if args.command == 'test-source-build':
        (BUILD / 'logs').mkdir(parents=True, exist_ok=True)
        native = 'windows-amd64' if os.name == 'nt' else 'linux-' + ('arm64' if platform.machine() in ['aarch64', 'arm64'] else 'amd64')
        directory = ROOT / 'dist' / (native + '-portable')
        env['CLIPROXY_PLUS_SOURCE_BUILD_TEST'] = '1'
        env['CLIPROXY_PLUS_SOURCE_BINARY_TEST'] = str(directory / ('cliproxyapi-plus.exe' if os.name == 'nt' else 'cliproxyapi-plus'))
        env['CLIPROXY_PLUS_SOURCE_KIT_TEST'] = str(directory / 'updater-source')
        tools = directory / 'update-tools.json'
        if args.tool_bundle:
            tools = BUILD / 'source-build-acceptance-tools.json'
            tools.write_text(json.dumps({'bundle': str(Path(args.tool_bundle).resolve())}), encoding='utf-8')
        if not tools.is_file(): raise ValueError('Build the complete bundled SDK package before source-build acceptance')
        env['CLIPROXY_PLUS_SOURCE_TOOLS_TEST'] = str(tools)
        receipt = BUILD / 'source-build-pipeline-report.json'
        env['CLIPROXY_PLUS_SOURCE_BUILD_REPORT'] = str(receipt)
        receipt.unlink(missing_ok=True)
        run([args.go, 'test', '-count=1', '-v', './internal/sourceupdate', '-run', '^TestBaselineSourceBuildPipeline$', '-timeout', '125m'], env=env)
        if not receipt.is_file() or not json.loads(receipt.read_text(encoding='utf-8')).get('passed'):
            raise ValueError('Full baseline source pipeline acceptance was skipped or failed')
        return
    if args.command == 'test-source':
        native = 'windows-amd64' if os.name == 'nt' else 'linux-' + ('arm64' if platform.machine() in ['aarch64', 'arm64'] else 'amd64')
        env['CLIPROXY_PLUS_SOURCE_KIT_TEST'] = str(ROOT / 'dist' / (native + '-portable') / 'updater-source')
        env['CLIPROXY_PLUS_BUNDLED_TOOLS_TEST'] = str(ROOT / 'dist' / (native + '-portable') / 'update-tools.json')
        if args.tool_bundle:
            config = BUILD / 'relocated-tools-test.json'
            config.write_text(json.dumps({'bundle': str(Path(args.tool_bundle).resolve())}), encoding='utf-8')
            env['CLIPROXY_PLUS_BUNDLED_TOOLS_TEST'] = str(config)
        env['CLIPROXY_PLUS_BUNDLED_TOOLS_REPORT'] = str(BUILD / 'bundled-tools-report.json')
        receipt = BUILD / 'source-update-kit-report.json'
        env['CLIPROXY_PLUS_SOURCE_KIT_REPORT'] = str(receipt)
        receipt.unlink(missing_ok=True)
        run([args.go, 'test', '-count=1', '-v', './internal/sourceupdate', '-run', '^TestPackaged(RecipeCrossLanguageVerification|SDKWithoutHostTools)$'], env=env)
        if not receipt.is_file() or not json.loads(receipt.read_text(encoding='utf-8')).get('passed'):
            raise ValueError('Packaged source recipe acceptance was skipped or failed')
        return
    if args.command == 'test-update':
        validate_web(prepared)
        env['CLIPROXY_PLUS_UPDATE_E2E'] = '1'
        native = 'windows-amd64' if os.name == 'nt' else 'linux-' + ('arm64' if platform.machine() in ['aarch64', 'arm64'] else 'amd64')
        env['CLIPROXY_PLUS_TEST_BINARY'] = str(ROOT / 'dist' / (native + '-portable') / ('cliproxyapi-plus.exe' if os.name == 'nt' else 'cliproxyapi-plus'))
        receipt = BUILD / 'web-update-smoke-report.json'
        receipt.unlink(missing_ok=True)
        run([args.go, 'test', '-count=1', '-v', './cmd/cliproxyapi-plus', '-run', '^TestWebUpdateLifecycle$'], env=env)
        if not receipt.is_file() or not json.loads(receipt.read_text(encoding='utf-8')).get('passed'):
            raise ValueError('Native web-update acceptance was skipped or produced no fresh success receipt')
        return
    if args.command == 'test-e2e':
        validate_web(prepared)
        env['CLIPROXY_PLUS_E2E'] = '1'
        run([args.go, 'test', '-count=1', '-v', './cmd/cliproxyapi-plus', '-run', 'TestSinglePortEndToEnd'], env=env)
        return
    if args.command in ['build-web', 'build']:
        if not args.skip_web:
            build_web(args, prepared, env)
        validate_web(prepared)
        if args.command == 'build-web':
            return
    if args.command in ['test', 'build']:
        if args.command == 'build': step('product_tests')
        run([sys.executable, '-m', 'unittest', 'discover', '-s', 'tests', '-p', 'test_*.py', '-v'])
        run([args.go, 'test', './...'], env=env)
        if args.command == 'test':
            return
        lock = lockfile()
        core = lock['projects']['cliproxyapi']
        manager = lock['projects']['cpa-manager-plus']
        import upstreams
        source_digest = upstreams.product_digest(ROOT)
        build_date = datetime.datetime.fromtimestamp(int(os.environ.get('SOURCE_DATE_EPOCH', str(int(datetime.datetime.now().timestamp())))), datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
        flags = '-s -w ' + ' '.join([
            '-X main.Version=' + VERSION, '-X main.SourceDigest=' + source_digest, '-X main.BuildDate=' + build_date, '-X main.LockDigest=' + hashlib.sha256((ROOT / 'upstream.lock.json').read_bytes()).hexdigest(),
            '-X github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo.BuildDate=' + build_date,
            '-X main.CoreVersion=' + core['ref'], '-X main.CoreCommit=' + core['commit'],
            '-X main.ManagerVersion=' + manager['ref'], '-X main.ManagerCommit=' + manager['commit'],
            '-X github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo.Version=' + core['ref'],
            '-X github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo.Commit=' + core['commit'],
            '-X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.Version=' + manager['ref'],
            '-X github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo.SourceCommit=' + manager['commit'],
        ])
        target = args.target or ('windows/amd64' if os.name == 'nt' else 'linux/' + ('arm64' if platform.machine() in ['aarch64', 'arm64'] else 'amd64'))
        if target not in ['windows/amd64', 'linux/amd64', 'linux/arm64']:
            raise ValueError('Unsupported release target: ' + target)
        goos, goarch = target.split('/')
        dist = ROOT / 'dist' / (target.replace('/', '-') + '-' + args.variant)
        dist.mkdir(parents=True, exist_ok=True)
        env = env.copy(); env['GOOS'] = goos; env['GOARCH'] = goarch
        binary = dist / ('cliproxyapi-plus.exe' if goos == 'windows' else 'cliproxyapi-plus')
        step('binary_build')
        run([args.go, 'build', '-buildvcs=false', '-trimpath', '-ldflags', flags, '-o', str(binary), './cmd/cliproxyapi-plus'], env=env)
        step('packaging')
        checksum = hashlib.sha256(binary.read_bytes()).hexdigest()
        (dist / 'checksums.txt').write_text(checksum + '  ' + binary.name + '\n', encoding='utf-8')
        import upstreams
        (dist / 'build-info.json').write_text(json.dumps({'version': VERSION, 'product_source_sha256': source_digest, 'build_date': build_date, 'frontend_toolchain': json.loads((prepared['cpa-manager-plus'] / '.plus-web.json').read_text(encoding='utf-8')).get('toolchain', {}), 'target': target, 'variant': args.variant, 'tree_hash_format': lock.get('tree_hash_format'), 'upstream_lock_sha256': hashlib.sha256((ROOT / 'upstream.lock.json').read_bytes()).hexdigest(), 'upstreams': lock['projects'], 'binary_sha256': checksum, 'go_version': subprocess.check_output([args.go, 'version'], text=True).strip()}, indent=2), encoding='utf-8')
        licenses = dist / 'licenses'
        licenses.mkdir(exist_ok=True)
        for name, path in prepared.items():
            shutil.copyfile(path / 'LICENSE', licenses / (name + '-LICENSE.txt'))
        shutil.copyfile(ROOT / 'upstream.lock.json', dist / 'upstream.lock.json')
        shutil.copyfile(ROOT / 'THIRD_PARTY_NOTICES.md', dist / 'THIRD_PARTY_NOTICES.md')
        shutil.copyfile(ROOT / 'README_CN.md', dist / 'README_CN.md')
        (dist / 'tools').mkdir(exist_ok=True)
        for name in ['data.py', 'upgrade.py', 'private.py', 'archive_paths.py']:
            shutil.copyfile(ROOT / 'scripts' / name, dist / 'tools' / name)
        (dist / 'docs').mkdir(exist_ok=True)
        for name in ['OPERATIONS_CN.md', 'IMPLEMENTATION_STATUS_CN.md', 'RELEASE_CHECKLIST_CN.md', 'WEB_UPDATES_CN.md', 'LOCAL_WEB_UPDATES_CN.md', 'RELEASE_SERVER_CN.md', 'SOURCE_UPDATES_CN.md']:
            shutil.copyfile(ROOT / 'docs' / name, dist / 'docs' / name)
        if not args.skip_sbom:
            run([sys.executable, str(ROOT / 'scripts/sbom.py'), '--go', args.go, '--output', str(dist)], env=environment(args))
        import toolchains
        sdk_executables = toolchains.package(dist, target, source=args.tool_bundle)
        (dist / 'update-tools.example.json').write_text(json.dumps({'python': '', 'go': '', 'node': '', 'npm_cli': ''}, indent=2) + '\n', encoding='utf-8')
        import source_kit
        source_kit.package(ROOT, dist / 'updater-source')
        archive = ROOT / 'dist' / ('CLIProxyAPIPlus_' + VERSION + '_' + target.replace('/', '_') + '_' + args.variant + ('.zip' if goos == 'windows' else '.tar.gz'))
        if goos == 'windows':
            with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as out:
                for file in sorted(dist.rglob('*')):
                    if file.is_file(): out.write(file, file.relative_to(dist).as_posix())
        else:
            write_release_tar(archive, dist, binary.name, executable_names=sdk_executables)
        archive_hash = hashlib.sha256(archive.read_bytes()).hexdigest()
        (archive.parent / (archive.name + '.sha256')).write_text(archive_hash + '  ' + archive.name + '\n', encoding='utf-8')
        print('Release-candidate built:', binary)


if __name__ == '__main__':
    from workspace_lock import file_lock
    try:
        with file_lock(BUILD / 'promotion.lock'):main()
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print('ERROR:', error, file=sys.stderr)
        sys.exit(1)