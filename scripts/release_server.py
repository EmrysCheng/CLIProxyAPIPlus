"""Build a standalone, Python-free local release-server distribution."""
import datetime
import hashlib
import json
import os
import platform
import shutil
import subprocess
import uuid
import zipfile
from pathlib import Path

import project
import release_index
import upstreams


def build(args, env):
    if args.variant != 'portable':
        raise ValueError('The standalone release server only supports portable builds')
    address = args.release_listen
    base_url = release_index.validate_base_url('http://' + address, True)
    # The URL must be exactly a loopback origin with a fixed port, not a path.
    from urllib.parse import urlparse
    parsed = urlparse(base_url)
    if parsed.path or not parsed.port or parsed.netloc != address:
        raise ValueError('release-listen must be a literal loopback IP and fixed port')
    source = upstreams.product_digest(project.ROOT)
    index, assets = release_index.generate(project.ROOT / 'dist', project.VERSION, base_url, allow_local_http=True, portable_only=True)
    if index['release']['product_source_sha256'] != source:
        raise ValueError('Product packages are stale; rebuild the current product version before packaging the release server')
    target = args.target or ('windows/amd64' if os.name == 'nt' else 'linux/' + ('arm64' if platform.machine() in ['aarch64', 'arm64'] else 'amd64'))
    if target not in ['windows/amd64', 'linux/amd64', 'linux/arm64']:
        raise ValueError('Unsupported release server target: ' + target)
    goos, goarch = target.split('/')
    stage = project.BUILD / 'release-server' / uuid.uuid4().hex
    stage.mkdir(parents=True)
    binary = stage / ('cliproxyapi-plus-release-server.exe' if goos == 'windows' else 'cliproxyapi-plus-release-server')
    build_date = datetime.datetime.fromtimestamp(int(os.environ.get('SOURCE_DATE_EPOCH', str(int(datetime.datetime.now().timestamp())))), datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    flags = '-s -w -X main.Version=' + project.VERSION + ' -X main.SourceDigest=' + source + ' -X main.BuildDate=' + build_date
    target_env = env.copy()
    target_env.update(GOOS=goos, GOARCH=goarch, CGO_ENABLED='0')
    project.run([args.go, 'build', '-buildvcs=false', '-trimpath', '-ldflags', flags, '-o', str(binary), './cmd/cliproxyapi-plus-release-server'], env=target_env)
    config = {'schema_version': 1, 'listen': address, 'release_dir': 'releases', 'log_file': 'logs/release-server.log', 'log_max_size_mb': 10}
    (stage / 'release-server.json').write_text(json.dumps(config, indent=2) + '\n', encoding='utf-8')
    release_index.publish(index, assets, stage / 'releases/index.json', copy_assets=True)
    if goos == 'windows':
        (stage / 'start.cmd').write_bytes(b'@echo off\r\nsetlocal\r\ncd /d "%~dp0"\r\n"%~dp0cliproxyapi-plus-release-server.exe" --config "%~dp0release-server.json"\r\nif errorlevel 1 pause\r\n')
    else:
        (stage / 'start.sh').write_text('#!/bin/sh\nset -eu\nSCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)\nexec "$SCRIPT_DIR/cliproxyapi-plus-release-server" --config "$SCRIPT_DIR/release-server.json"\n', encoding='utf-8', newline='\n')
    if goos != 'windows':
        binary.chmod(0o755)
        (stage / 'start.sh').chmod(0o755)
    shutil.copyfile(project.ROOT / 'docs/RELEASE_SERVER_CN.md', stage / 'README_CN.md')
    goroot = Path(subprocess.check_output([args.go, 'env', 'GOROOT'], env=env, text=True).strip())
    (stage / 'licenses').mkdir()
    shutil.copyfile(goroot / 'LICENSE', stage / 'licenses/Go-LICENSE.txt')
    (stage / 'THIRD_PARTY_NOTICES.md').write_text('The release-server executable uses this project and the Go standard library. See licenses/Go-LICENSE.txt. Each integrated application archive in releases/ retains its own upstream licenses, SBOM and dependency notices. No Python, Node or Go installation is required at runtime.\n', encoding='utf-8')
    dependencies = subprocess.check_output([args.go, 'list', '-deps', '-f', '{{if not .Standard}}{{.ImportPath}}{{end}}', './cmd/cliproxyapi-plus-release-server'], env=target_env, text=True).split()
    if any(not x.startswith('cliproxyapi-plus/') for x in dependencies):
        raise ValueError('Release-server dependency scope changed; review runtime licenses before packaging')
    info = {'role': 'local-release-server', 'version': project.VERSION, 'product_source_sha256': source, 'build_date': build_date, 'target': target, 'variant': 'portable', 'binary_sha256': release_index.digest(binary), 'go_version': subprocess.check_output([args.go, 'version'], text=True).strip(), 'project_dependencies': dependencies, 'bundled_assets': index['release']['assets']}
    (stage / 'build-info.json').write_text(json.dumps(info, indent=2) + '\n', encoding='utf-8')
    sums = [release_index.digest(f) + '  ' + f.relative_to(stage).as_posix() for f in sorted(stage.rglob('*')) if f.is_file()]
    (stage / 'checksums.txt').write_text('\n'.join(sums) + '\n', encoding='utf-8')
    archive = project.ROOT / 'dist' / ('CLIProxyAPIPlus_ReleaseServer_' + project.VERSION + '_' + target.replace('/', '_') + '_portable' + ('.zip' if goos == 'windows' else '.tar.gz'))
    if goos == 'windows':
        with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as packed:
            for file in sorted(stage.rglob('*')):
                if file.is_file():
                    compressed = file.name.endswith(('.zip', '.tar.gz'))
                    packed.write(file, file.relative_to(stage).as_posix(), compress_type=zipfile.ZIP_STORED if compressed else zipfile.ZIP_DEFLATED)
    else:
        project.write_release_tar(archive, stage, binary.name, executable_names={'start.sh'})
    archive.with_name(archive.name + '.sha256').write_text(release_index.digest(archive) + '  ' + archive.name + '\n', encoding='utf-8')
    destination = project.ROOT / 'dist' / ('release-server-' + target.replace('/', '-') + '-portable')
    # Never delete an existing checkout/deployment or include its old runtime logs.
    # The archive is assembled solely from our fresh private stage.
    shutil.copytree(stage, destination, dirs_exist_ok=True)
    print('Standalone release-server candidate built:', destination / binary.name)
    print('Prepared index:', destination / 'releases/index.json')
