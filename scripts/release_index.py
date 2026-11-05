#!/usr/bin/env python3
"""Generate verified product metadata; optionally copy assets, never run or sign them."""
import argparse
import hashlib
import ipaddress
import json
import os
import re
import shutil
import tarfile
import tempfile
import time
import zipfile
from pathlib import Path
from urllib.parse import quote, urlparse

import project


def validate_base_url(raw, allow_local_http=False):
    u = urlparse(raw)
    port = u.port  # Reject malformed/out-of-range ports before publishing metadata.
    try:
        loopback = bool(u.hostname) and '%' not in u.hostname and ipaddress.ip_address(u.hostname).is_loopback
    except ValueError:
        loopback = False
    if (not u.netloc or not u.hostname or u.username is not None or u.password is not None
            or u.query or u.fragment or (port is not None and not 0 < port < 65536)):
        raise ValueError('Base URL must be absolute and contain no credentials/query/fragment')
    if u.scheme != 'https' and not (allow_local_http and u.scheme == 'http' and loopback):
        raise ValueError('Base URL requires HTTPS; local HTTP requires --allow-local-release-http and a literal loopback IP')
    return raw.rstrip('/')


def digest(path):
    with path.open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()


def build_info(path):
    if path.name.endswith('.zip'):
        with zipfile.ZipFile(path) as archive:
            entries = [x for x in archive.infolist() if x.filename == 'build-info.json']
            if len(entries) != 1 or entries[0].file_size > 131072:
                raise ValueError('Missing, duplicate or oversized build-info.json: ' + path.name)
            raw = archive.read(entries[0])
    else:
        with tarfile.open(path, 'r:gz') as archive:
            entries = [x for x in archive if x.name == 'build-info.json']
            if len(entries) != 1 or not entries[0].isfile() or entries[0].size > 131072:
                raise ValueError('Missing, duplicate or oversized build-info.json: ' + path.name)
            with archive.extractfile(entries[0]) as f:
                raw = f.read(131073)
    return json.loads(raw)


def generate(dist, version, base_url, source_commit=None, allow_local_http=False, notes='', migration_required=False, portable_only=False):
    base = validate_base_url(base_url, allow_local_http)
    if source_commit is not None and not re.fullmatch('[0-9a-f]{40}', source_commit):
        raise ValueError('source-commit must be a real 40-character product Git commit, never a made-up value')
    files = sorted(x for x in dist.glob('CLIProxyAPIPlus_' + version + '_*') if x.name.endswith(('.zip', '.tar.gz')) and (not portable_only or x.name.endswith(('_portable.zip', '_portable.tar.gz'))))
    if not files:
        raise ValueError('Build verified release archives first')
    assets, sources = [], set()
    for f in files:
        checksum = digest(f)
        expected = f.with_name(f.name + '.sha256')
        if not expected.is_file() or not expected.read_text(encoding='utf-8').split() or expected.read_text(encoding='utf-8').split()[0] != checksum:
            raise ValueError('Archive checksum mismatch: ' + f.name)
        info = build_info(f)
        source = info.get('product_source_sha256', '')
        if info.get('version') != version or not re.fullmatch('[0-9a-f]{64}', source):
            raise ValueError('Package version/source identity invalid: ' + f.name)
        sources.add(source)
        assets.append({'name': f.name, 'sha256': checksum, 'url': base + '/' + quote(f.name)})
    if len(sources) != 1:
        raise ValueError('Packages were built from different product sources; rebuild the release together')
    release = {'version': version, 'product_source_sha256': sources.pop(), 'migration_required': migration_required, 'notes': notes, 'assets': assets}
    if source_commit is not None:
        release['source_commit'] = source_commit
    return {'schema_version': 1, 'release': release}, files


def publish(index, files, output, copy_assets=False, replace=False):
    output = output.resolve()
    if any(output == source.resolve() for source in files):
        raise ValueError('Index must not overwrite a release package')
    if output.exists() and not replace:
        raise FileExistsError('Index already exists; use --replace to atomically publish a new index')
    output.parent.mkdir(parents=True, exist_ok=True)
    if copy_assets:
        for source in files:
            target = output.parent / source.name
            if target.resolve() == source.resolve():
                continue
            if target.exists():
                if digest(target) != digest(source):
                    raise FileExistsError('Different published package already exists: ' + str(target))
            else:
                with target.open('xb') as dest, source.open('rb') as src:
                    shutil.copyfileobj(src, dest)
    raw = json.dumps(index, indent=2, ensure_ascii=False) + '\n'
    if not replace:
        with output.open('x', encoding='utf-8') as f:
            f.write(raw)
    else:
        # Publish the index last so clients never observe a partial JSON document.
        fd, temporary = tempfile.mkstemp(prefix='.index-', suffix='.tmp', dir=output.parent)
        try:
            with os.fdopen(fd, 'w', encoding='utf-8') as f:
                f.write(raw)
                f.flush()
                os.fsync(f.fileno())
            # Windows static servers may briefly hold a reader without delete
            # sharing. Retry sharing/ACL errors finitely; do not publish in-place.
            deadline = time.monotonic() + 5
            while True:
                try:
                    os.replace(temporary, output)
                    break
                except PermissionError:
                    if os.name != 'nt' or time.monotonic() >= deadline:
                        raise
                    time.sleep(0.1)
        finally:
            Path(temporary).unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-url', required=True)
    parser.add_argument('--allow-local-release-http', action='store_true', help='Only literal loopback HTTP; explicitly opt in on the application too')
    parser.add_argument('--source-commit', help='Optional real product Git commit; package source SHA256 is always checked and recorded')
    parser.add_argument('--dist-dir', type=Path, default=project.ROOT / 'dist')
    parser.add_argument('--version', default=project.VERSION, help='Must match version recorded inside every selected package')
    parser.add_argument('--notes', default='')
    parser.add_argument('--migration-required', action='store_true')
    parser.add_argument('--copy-assets', action='store_true', help='Copy checksum-verified packages beside the index')
    parser.add_argument('--replace', action='store_true', help='Atomically replace an existing index; never replace a different package')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    index, files = generate(args.dist_dir, args.version, args.base_url, args.source_commit, args.allow_local_release_http, args.notes, args.migration_required)
    publish(index, files, args.output, args.copy_assets, args.replace)
    print('Index generated locally, not signed or uploaded:', args.output.resolve())
    print('Product source SHA256:', index['release']['product_source_sha256'])
    if args.allow_local_release_http:
        print('Application must also use --allow-local-release-http. Bind the file server to the same loopback IP.')


if __name__ == '__main__':
    main()
