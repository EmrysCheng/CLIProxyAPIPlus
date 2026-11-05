#!/usr/bin/env python3
"""Private source build worker. Never a network service or an arbitrary command API."""
import argparse
import hashlib
import json
import os
import re
import sys
import subprocess
import time
from pathlib import Path


def phase(value):
    print('PLUS_SOURCE_PHASE:' + value, flush=True)


def check_node_engine(source, tools):
    package = json.loads((source / 'upstream/cpa-manager-plus/package.json').read_text(encoding='utf-8'))
    required = package.get('engines', {}).get('node')
    if not required:
        return
    npm_root = str(Path(tools['npm_cli']).resolve().parents[1])
    # The expression is constant; the untrusted upstream range is a separate
    # argument, never interpolated into JavaScript or a shell command.
    expression = "const semver=require(require.resolve('semver',{paths:[process.argv[2]]}));if(!semver.satisfies(process.version,process.argv[1])){console.error('Required Node engine: '+process.argv[1]+'; actual '+process.version);process.exit(1)}"
    subprocess.run([tools['node'], '-e', expression, required, npm_root], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--request', required=True, type=Path)
    parser.add_argument('--result', required=True, type=Path)
    parser.add_argument('--gate', required=True, type=Path)
    args = parser.parse_args()
    # The Go parent attaches the process to a kill-on-close job / process group
    # before opening this gate. No subprocess or upstream build starts earlier.
    deadline = time.monotonic() + 30
    while not args.gate.is_file():
        if time.monotonic() >= deadline:
            raise TimeoutError('Build process ownership gate did not open')
        time.sleep(0.02)
    request = json.loads(args.request.read_text(encoding='utf-8'))
    workspace = Path(__file__).resolve().parents[1]
    sys.path.insert(0, str(workspace / 'scripts'))
    import project
    import upstreams
    import source_kit
    from types import SimpleNamespace
    if upstreams.product_digest(workspace) != request['product_source_sha256']:
        raise ValueError('Recipe source identity changed before build')
    lock = project.lockfile()
    fixed = {'cliproxyapi': 'router-for-me/CLIProxyAPI', 'cpa-manager-plus': 'seakee/CPA-Manager-Plus'}
    if hashlib.sha256((workspace / 'upstream.lock.json').read_bytes()).hexdigest() != request['upstream_lock_sha256']:
        raise ValueError('Recipe lock changed')
    for name, repository in fixed.items():
        if lock['projects'][name]['repository'] != repository or lock['projects'][name]['commit'] != request['current'][name]['commit']:
            raise ValueError('Recipe upstream identities do not match running binary')
    targets = request['targets']
    if not targets or set(targets) - set(fixed):
        raise ValueError('Unknown source project')
    pins = {}
    for name, target in targets.items():
        if not re.fullmatch('[0-9a-f]{40}', target['commit']) or not re.fullmatch('[A-Za-z0-9._-]{1,128}', target['version']):
            raise ValueError('Source request is not commit-pinned')
        pins[name] = target
    os.environ['PLUS_SOURCE_BUILD_PROGRESS']='1'
    phase('fetching_source')
    candidate = upstreams.stage(SimpleNamespace(project='all', channel='stable', ref=None, offline=False, pins=pins))
    # stage keeps the unselected partner exactly at the running commit; no live
    # upstream/ directory is ever promoted or edited by this workflow.
    source = candidate / 'source'
    candidate_lock = json.loads((source / 'upstream.lock.json').read_text(encoding='utf-8'))
    for name in fixed:
        expected = pins.get(name, request['current'][name])
        if candidate_lock['projects'][name]['commit'] != expected['commit']:
            raise ValueError('Staged source does not match confirmed pair')
    phase('validating_source')
    tools = request['tools']
    check_node_engine(source, tools)
    upstreams.validate(SimpleNamespace(candidate=str(candidate), go=tools['go'], node=tools['node'], npm_cli=tools.get('npm_cli') or None, tool_bundle=tools.get('bundle') or None))
    receipt = json.loads((candidate / 'validation.json').read_text(encoding='utf-8'))
    required = {'build', 'test-e2e', 'test-update', 'test-source', 'test-web', 'regression', 'smoke_binary.py', 'smoke_maintenance.py'}
    if not receipt.get('passed') or not required.issubset(set(receipt.get('commands', []))) or receipt['candidate_lock_sha256'] != hashlib.sha256((source / 'upstream.lock.json').read_bytes()).hexdigest():
        raise ValueError('No complete, current validation receipt')
    phase('verifying_artifact')
    source_kit.write_manifest(source)
    target = request['target']
    name = 'CLIProxyAPIPlus_' + project.VERSION + '_' + target.replace('/', '_') + '_portable' + ('.zip' if target.startswith('windows/') else '.tar.gz')
    archive = source / 'dist' / name
    if not archive.is_file():
        raise ValueError('Validated native Portable archive is missing')
    info = json.loads((source / 'dist' / (target.replace('/', '-') + '-portable') / 'build-info.json').read_text(encoding='utf-8'))
    result = {'archive': str(archive.resolve()), 'name': name, 'sha256': hashlib.sha256(archive.read_bytes()).hexdigest(), 'product_source_sha256': info['product_source_sha256'], 'workspace': str(source.resolve()), 'core_commit': candidate_lock['projects']['cliproxyapi']['commit'], 'manager_commit': candidate_lock['projects']['cpa-manager-plus']['commit'], 'version': info['version']}
    pending = args.result.with_suffix('.pending')
    pending.write_text(json.dumps(result), encoding='utf-8')
    os.replace(pending, args.result)


if __name__ == '__main__':
    main()
