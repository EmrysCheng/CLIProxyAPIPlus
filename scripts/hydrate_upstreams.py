#!/usr/bin/env python3
"""Materialize absent upstream snapshots from exact, hash-locked public commits.

This is a source-checkout/build step, never an application startup action.
Existing snapshots are verified and never replaced or repaired in place.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import stat
import tempfile
import urllib.request

import project
import upstreams
from workspace_lock import file_lock

ROOT = Path(__file__).resolve().parents[1]
REPOSITORIES = {
    'cliproxyapi': 'router-for-me/CLIProxyAPI',
    'cpa-manager-plus': 'seakee/CPA-Manager-Plus',
}
TREE_FORMAT = 'sha256-length-prefixed-posix-utf8-order-v2'
MAX_COMPRESSED = 128 * 1024 * 1024


def real_directory(path: Path, *, create: bool = False) -> None:
    try:
        info = path.lstat()
    except FileNotFoundError:
        if not create:
            raise
        path.mkdir()
        info = path.lstat()
    if (not stat.S_ISDIR(info.st_mode) or stat.S_ISLNK(info.st_mode)
            or getattr(info, 'st_file_attributes', 0) & 0x400):
        raise ValueError('Source directories must be real directories, not links/reparse points: ' + str(path))


def validate_lock(root: Path) -> dict:
    lock = json.loads((root / 'upstream.lock.json').read_text(encoding='utf-8'))
    if (lock.get('schema_version') != 1 or lock.get('tree_hash_format') != TREE_FORMAT
            or not isinstance(lock.get('projects'), dict)
            or set(lock['projects']) != set(REPOSITORIES)):
        raise ValueError('Unsupported or incomplete immutable upstream lock')
    for name, item in lock['projects'].items():
        if (not isinstance(item, dict) or item.get('repository') != REPOSITORIES[name]
                or not isinstance(item.get('commit'), str)
                or not re.fullmatch('[0-9a-f]{40}', item['commit'])
                or not isinstance(item.get('tree_sha256'), str)
                or not re.fullmatch('[0-9a-f]{64}', item['tree_sha256'])):
            raise ValueError('Unapproved or unpinned upstream identity: ' + name)
    return lock


def verify_snapshot(path: Path, name: str, item: dict) -> None:
    real_directory(path)
    for child in path.rglob('*'):
        info = child.lstat()
        if (stat.S_ISLNK(info.st_mode) or getattr(info, 'st_file_attributes', 0) & 0x400
                or not (stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode))):
            raise ValueError('Source snapshots cannot contain links, reparse points, or special files')
    if project.tree_digest(path) != item['tree_sha256']:
        raise ValueError('Immutable upstream snapshot checksum mismatch; refusing to replace: ' + name)
    if not (path / 'LICENSE').is_file():
        raise ValueError('Upstream LICENSE is missing: ' + name)
    relative = 'go.mod' if name == 'cliproxyapi' else 'apps/manager-server/go.mod'
    module = ('github.com/router-for-me/CLIProxyAPI/v8' if name == 'cliproxyapi'
              else 'github.com/seakee/cpa-manager-plus/apps/manager-server')
    if not (path / relative).read_text(encoding='utf-8').startswith('module ' + module + '\n'):
        raise ValueError('Upstream module identity mismatch: ' + name)


def hydrate(root: Path = ROOT, *, offline: bool = False) -> dict:
    root = root.resolve()
    lock = validate_lock(root)  # Validate both pins before any network access.
    real_directory(root)
    build = root / '.build'
    real_directory(build, create=True)
    guard = build / 'promotion.lock'
    try:
        info = guard.lstat()
    except FileNotFoundError:
        pass
    else:
        if not stat.S_ISREG(info.st_mode) or getattr(info, 'st_file_attributes', 0) & 0x400:
            raise ValueError('Workspace lock must be a regular non-link file')
    with file_lock(guard):
        parent = root / 'upstream'
        real_directory(parent, create=True)
        missing = []
        for name, item in lock['projects'].items():
            target = parent / name
            # lstat sees dangling links as well, unlike exists().
            try:
                target.lstat()
            except FileNotFoundError:
                missing.append(name)
            else:
                verify_snapshot(target, name, item)
        if offline and missing:
            raise ValueError('Offline checkout is missing locked snapshots: ' + ', '.join(missing))
        # Stage and verify every missing snapshot before publishing any of them.
        with tempfile.TemporaryDirectory(prefix='hydrate-', dir=build) as temporary:
            stage = Path(temporary)
            for name in missing:
                item = lock['projects'][name]
                request = urllib.request.Request(
                    'https://codeload.github.com/' + item['repository'] + '/zip/' + item['commit'],
                    headers={'User-Agent': 'CLIProxyAPIPlus-locked-source-hydration'},
                )
                # No GitHub token, moving tag lookup, shell, or SDK download here.
                with upstreams.open_github(request, timeout=90) as response:
                    data = response.read(MAX_COMPRESSED + 1)
                if len(data) > MAX_COMPRESSED:
                    raise ValueError('Compressed source archive exceeds limit: ' + name)
                destination = stage / name
                destination.mkdir()
                upstreams.safe_extract(data, destination)
                verify_snapshot(destination, name, item)
            for name in missing:
                target = parent / name
                if os.path.lexists(target):
                    raise ValueError('Upstream destination appeared during hydration; refusing to replace: ' + name)
                (stage / name).rename(target)
        return {'verified': list(REPOSITORIES), 'downloaded': missing, 'offline': offline}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--offline', action='store_true', help='Verify existing snapshots; never download')
    args = parser.parse_args()
    try:
        print(json.dumps(hydrate(offline=args.offline), indent=2))
    except (OSError, ValueError) as error:
        parser.exit(1, 'Locked source hydration failed: ' + str(error) + '\n')
