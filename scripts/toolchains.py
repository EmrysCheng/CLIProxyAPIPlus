"""Pinned, redistributable build SDKs. Downloads happen at package build, never at app start."""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import posixpath
import shutil
import stat
import tarfile
import tempfile
import urllib.parse
import urllib.request
import zipfile
import archive_paths

ROOT = Path(__file__).resolve().parents[1]
LOCK = ROOT / 'internal/sourceupdate/toolchains.lock.json'
MAX_DOWNLOAD = 256 << 20
MAX_UNPACKED = 1 << 30


def sha(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def read_lock():
    data = json.loads(LOCK.read_text(encoding='utf-8'))
    if data['schema_version'] != 1 or set(data['platforms']) != {'windows/amd64', 'linux/amd64', 'linux/arm64'}:
        raise ValueError('Invalid bundled toolchain lock')
    return data


def download(item, cache):
    cache.mkdir(parents=True, exist_ok=True)
    suffix = '.zip' if (item.get('format') == 'zip' or item['url'].endswith('.zip')) else '.tar.gz'
    path = cache / (item['sha256'] + suffix)
    if path.exists():
        if sha(path) != item['sha256']:
            raise ValueError('Cached SDK checksum mismatch; remove the corrupt archive explicitly')
        return path
    url = urllib.parse.urlsplit(item['url'])
    if url.scheme != 'https' or url.username or url.password or url.hostname not in {'go.dev', 'nodejs.org', 'github.com', 'codeload.github.com'}:
        raise ValueError('SDK download is not from an approved HTTPS publisher')
    request = urllib.request.Request(item['url'], headers={'User-Agent': 'CLIProxyAPIPlus-build'})
    # A temporary file is never accepted as a cache hit; only verified bytes are renamed.
    with tempfile.NamedTemporaryFile(dir=cache, delete=False) as pending:
        temporary = Path(pending.name)
        try:
            with urllib.request.urlopen(request, timeout=120) as response:
                total = 0
                while block := response.read(1 << 20):
                    total += len(block)
                    if total > MAX_DOWNLOAD:
                        raise ValueError('SDK download exceeds limit')
                    pending.write(block)
            pending.close()
            if sha(temporary) != item['sha256']:
                raise ValueError('SDK publisher archive checksum mismatch')
            temporary.replace(path)
        finally:
            pending.close()
            temporary.unlink(missing_ok=True)
    return path


def extract(archive, destination, prefix, only=None, exclude_prefixes=()):
    """Materialize in-archive links as regular files; never create filesystem links."""
    is_zip = archive.suffix == '.zip'
    opener = zipfile.ZipFile if is_zip else tarfile.open
    with opener(archive) as packed:
        items = packed.infolist() if is_zip else packed.getmembers()
        if len(items) > 40000:
            raise ValueError('SDK archive entry limit')
        entries = {}
        folded = set()
        for item in items:
            name = item.filename if is_zip else item.name
            directory = item.is_dir() if is_zip else item.isdir()
            archive_paths.validate(name, directory)
            if directory:
                continue
            if not name.startswith(prefix):
                raise ValueError('SDK archive escapes its single expected prefix')
            relative = name[len(prefix):]
            archive_paths.validate(relative)
            if (only is not None and relative not in only) or any(relative.startswith(p) for p in exclude_prefixes):
                continue
            if relative.casefold() in folded:
                raise ValueError('SDK archive path collision')
            folded.add(relative.casefold())
            mode = (item.external_attr >> 16) if is_zip else item.mode
            if is_zip and stat.S_ISLNK(mode):
                raise ValueError('ZIP SDK links are not supported')
            if not is_zip and not (item.isfile() or item.issym() or item.islnk()):
                raise ValueError('SDK archive contains a special file')
            entries[name] = (item, relative, mode)
        def resolve(name, seen):
            if name in seen or len(seen) > 16 or name not in entries:
                raise ValueError('Invalid SDK archive link')
            item, relative, mode = entries[name]
            if not is_zip and (item.issym() or item.islnk()):
                target = posixpath.normpath(posixpath.join(posixpath.dirname(name), item.linkname) if item.issym() else item.linkname)
                if not target.startswith(prefix) or target.startswith('/') or '\\' in target:
                    raise ValueError('SDK link escapes its archive prefix')
                return resolve(target, seen | {name})
            return item, mode
        total = 0
        executables = []
        for name, (_, relative, _) in entries.items():
            item, mode = resolve(name, set())
            size = item.file_size if is_zip else item.size
            total += size
            if size < 0 or total > MAX_UNPACKED:
                raise ValueError('SDK expanded size limit')
            target = destination / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            reader = packed.open(item) if is_zip else packed.extractfile(item)
            with reader, target.open('xb') as out:
                shutil.copyfileobj(reader, out)
            if target.stat().st_size != size:
                raise ValueError('SDK entry size mismatch')
            executable = bool(mode & 0o111)
            target.chmod(0o755 if executable else 0o644)
            if executable:
                executables.append(relative)
    return executables


def paths(target):
    windows = target.startswith('windows/')
    return {'python': 'python/python.exe' if windows else 'python/bin/python3',
            'go': 'go/bin/go.exe' if windows else 'go/bin/go',
            'node': 'node/node.exe' if windows else 'node/bin/node',
            'npm_cli': 'node/node_modules/npm/bin/npm-cli.js' if windows else 'node/lib/node_modules/npm/bin/npm-cli.js'}


def inventory(root):
    result = {}
    for path in sorted(root.rglob('*')):
        if path.is_symlink() or getattr(path, 'is_junction', lambda: False)():
            raise ValueError('Bundled SDK cannot contain filesystem links')
        if path.is_file() and path.relative_to(root).as_posix() != 'toolchain-manifest.json':
            name = path.relative_to(root).as_posix()
            archive_paths.validate(name)
            result[name] = sha(path)
    return result


def verify(root, target, lock=None):
    root = Path(root).absolute()
    lock = lock or read_lock()
    expected = lock['platforms'][target]['manifest_sha256']
    manifest_path = root / 'toolchain-manifest.json'
    if root.is_symlink() or getattr(root, 'is_junction', lambda: False)() or not expected or sha(manifest_path) != expected:
        raise ValueError('Bundled SDK manifest does not match the product-pinned digest')
    data = json.loads(manifest_path.read_text(encoding='utf-8'))
    if data['schema_version'] != 1 or data['target'] != target or data['tools'] != paths(target):
        raise ValueError('Bundled SDK platform/tool paths mismatch')
    if inventory(root) != data['records']:
        raise ValueError('Bundled SDK files are missing, changed or unlisted')
    if not target.startswith('windows/') and os.name != 'nt':
        for name in data['executables']:
            if not (root / name).stat().st_mode & 0o111:
                raise ValueError('Bundled SDK lost executable permissions: ' + name)
    return data


def prepare(target, *, seal=False):
    lock = read_lock()
    item = lock['platforms'][target]
    cache = ROOT / '.build/toolchain-downloads'
    # The publisher archive hashes, not mutable version tags, identify this cache.
    key = hashlib.sha256(json.dumps(item['archives'], sort_keys=True).encode()).hexdigest()[:20]
    directory = ROOT / '.build/toolchains' / (target.replace('/', '-') + '-' + key)
    if not directory.exists():
        directory.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix='sdk-stage-', dir=directory.parent) as temporary:
            staged = Path(temporary) / 'bundle'; staged.mkdir()
            executable = []
            for name, archive in item['archives'].items():
                print('Preparing bundled SDK:', target, name, flush=True)
                executable += [name + '/' + p for p in extract(download(archive, cache), staged / name, archive['prefix'], archive.get('only'), archive.get('exclude_prefixes', ()))]
            npm_path = staged / paths(target)['npm_cli']
            npm_version = json.loads((npm_path.parent.parent / 'package.json').read_text(encoding='utf-8'))['version']
            data = {'schema_version': 1, 'target': target, 'tools': paths(target),
                    'versions': {**lock['versions'], 'npm': npm_version}, 'records': inventory(staged), 'executables': sorted(executable)}
            (staged / 'toolchain-manifest.json').write_text(json.dumps(data, indent=2, sort_keys=True) + '\n', encoding='utf-8', newline='\n')
            staged.rename(directory)
    if seal:
        # Maintainer-only explicit command. Normal builds NEVER update trusted pins.
        item['manifest_sha256'] = sha(directory / 'toolchain-manifest.json')
        LOCK.write_text(json.dumps(lock, indent=2) + '\n', encoding='utf-8', newline='\n')
    verify(directory, target)
    return directory


def package(destination, target, source=None):
    source = Path(source) if source else prepare(target)
    data = verify(source, target)
    target_root = destination / 'build-tools'
    if target_root.exists():
        verify(target_root, target)
    else:
        shutil.copytree(source, target_root)
        verify(target_root, target)
    # Operator-requested package default; the local config remains editable.
    (destination / 'update-tools.json').write_text(
        json.dumps({'bundle': './build-tools',
                    'go_proxy': 'https://goproxy.cn|https://proxy.golang.org'}, indent=2) + '\n',
        encoding='utf-8', newline='\n')
    (destination / 'licenses').mkdir(exist_ok=True)
    (destination / 'licenses/BUNDLED_TOOLS_CN.md').write_text(
        '# 随包构建工具\n\n仅在发行包构建时下载固定发行物；运行时不下载 SDK，不安装到系统 PATH。\n'
        'Go/Node 采用官方可再分发归档，Python 采用 Astral python-build-standalone。\n'
        '每份 SDK 的 LICENSE、NOTICE、share/licenses 等原始文件均保留在 build-tools 下。\n'
        'archive SHA256、来源、目标平台见 updater-source/internal/sourceupdate/toolchains.lock.json；'
        '逐文件 SHA256、版本与权限见 build-tools/toolchain-manifest.json。\n', encoding='utf-8', newline='\n')
    return {'build-tools/' + name for name in data['executables']}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['prepare', 'seal'])
    parser.add_argument('--target', required=True, choices=['windows/amd64', 'linux/amd64', 'linux/arm64'])
    args = parser.parse_args()
    print(prepare(args.target, seal=args.command == 'seal'))
