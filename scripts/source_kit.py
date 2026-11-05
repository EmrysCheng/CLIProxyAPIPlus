"""Prepare a hash-bound, immutable source recipe for in-process web updates."""
import hashlib
import json
import shutil
from pathlib import Path
import project
import upstreams


def manifest(root):
    root = root.resolve()
    records = {}
    product_records = {}
    for name in upstreams.PRODUCT_PATHS:
        path = root / name
        files = [path] if path.is_file() else path.rglob('*') if path.exists() else []
        for file in files:
            if not file.is_file() or '__pycache__' in file.parts:
                continue
            if file.is_symlink():
                raise ValueError('Source recipes cannot contain links')
            relative = file.relative_to(root).as_posix()
            if not relative.isascii():
                raise ValueError('Source recipe product filenames must be portable ASCII')
            records[relative] = product_records[relative] = hashlib.sha256(file.read_bytes()).hexdigest()
    lock = json.loads((root / 'upstream.lock.json').read_text(encoding='utf-8'))
    for name, item in lock['projects'].items():
        if project.tree_digest(root / 'upstream' / name) != item['tree_sha256']:
            raise ValueError('Immutable upstream snapshot changed: ' + name)
        for file in (root / 'upstream' / name).rglob('*'):
            if file.is_symlink():
                raise ValueError('Source snapshots cannot contain links')
            if file.is_file():
                records[file.relative_to(root).as_posix()] = hashlib.sha256(file.read_bytes()).hexdigest()
    records['upstream.lock.json'] = hashlib.sha256((root / 'upstream.lock.json').read_bytes()).hexdigest()
    return {'schema_version': 1, 'product_source_sha256': hashlib.sha256(json.dumps(product_records, sort_keys=True).encode()).hexdigest(), 'upstream_lock_sha256': records['upstream.lock.json'], 'product_records': product_records, 'records': records}


def write_manifest(root):
    data = manifest(root)
    (root / 'source-kit.json').write_text(json.dumps(data, indent=2, sort_keys=True) + '\n', encoding='utf-8')
    return data


def package(root, destination):
    data = manifest(root)
    destination.mkdir(parents=True, exist_ok=True)
    for name in data['records']:
        target = project.child(destination, name)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(root / name, target)
    (destination / 'source-kit.json').write_text(json.dumps(data, indent=2, sort_keys=True) + '\n', encoding='utf-8')
    return data
