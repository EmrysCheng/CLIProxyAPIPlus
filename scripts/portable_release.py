#!/usr/bin/env python3
"""Explicit, native-only Portable release gates; never execute downloaded assets."""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import urllib.error
import urllib.request
import zipfile
import archive_paths
import source_kit

ROOT = Path(__file__).resolve().parents[1]
TARGETS = ('windows/amd64', 'linux/amd64', 'linux/arm64')
REPO = 'EmrysCheng/CLIProxyAPIPlus'
DOCS = ('OPERATIONS_CN.md', 'IMPLEMENTATION_STATUS_CN.md', 'RELEASE_CHECKLIST_CN.md', 'WEB_UPDATES_CN.md', 'LOCAL_WEB_UPDATES_CN.md', 'RELEASE_SERVER_CN.md', 'SOURCE_UPDATES_CN.md')
TOOLS = ('data.py', 'upgrade.py', 'private.py', 'archive_paths.py')
NATIVE_CHECKS = ['binary-smoke-report.json', 'maintenance-smoke-report.json', 'source-update-kit-report.json', 'bundled-tools-report.json']


def public_json(raw):
    def unique_members(pairs):
        value = {}
        for key,item in pairs:
            if key in value:
                raise ValueError('Duplicate JSON object member in public artifact')
            value[key] = item
        return value
    def invalid_constant(_value):
        raise ValueError('Non-finite JSON constant in public artifact')
    return json.loads(raw,object_pairs_hook=unique_members,parse_constant=invalid_constant)


def digest(path):
    with path.open('rb') as reader:
        return hashlib.file_digest(reader, 'sha256').hexdigest()


def release_version(tag):
    number = r'(?:0|[1-9][0-9]*)'
    if not re.fullmatch('v' + number + r'\.' + number + r'\.' + number, tag):
        raise ValueError('A canonical stable vX.Y.Z tag is required')
    return tag[1:]


def archive_name(version, target):
    return 'CLIProxyAPIPlus_' + version + '_' + target.replace('/', '_') + '_portable' + ('.zip' if target.startswith('windows/') else '.tar.gz')


def read_notices_lock():
    return public_json((ROOT/'internal/sourceupdate/portable-notices.lock.json').read_text())


def verify_licenses(hashes, index_raw, recipe, target):
    # Dependency notice bytes must match public content pinned in the reviewed
    # checkout. The package's own index is not an authority for arbitrary hashes.
    lock = read_notices_lock()
    index = public_json(index_raw)
    if not isinstance(index,list) or len(index)>2000:
        raise ValueError('Invalid dependency notice index')
    metadata = [{key:value for key,value in entry.items() if key!='notices'} for entry in index]
    if hashlib.sha256(json.dumps(metadata,sort_keys=True).encode()).hexdigest() != lock['dependency_metadata_sha256']:
        raise ValueError('Dependency notice metadata differs from trusted lock')
    rules = lock['platforms'][target]
    keys = {hashlib.sha256((e['ecosystem']+':'+e['name']+'@'+e['version']).encode()).hexdigest()[:24] for e in index}
    if set(rules) != keys:
        raise ValueError('Trusted notice lock does not match the component inventory')
    expected = {'licenses/'+name+'-LICENSE.txt':recipe['records']['upstream/'+name+'/LICENSE'] for name in ['cliproxyapi','cpa-manager-plus']}
    bundled_notice_hash = hashes.get('licenses/BUNDLED_TOOLS_CN.md')
    if bundled_notice_hash in (
        lock['bundled_notice_sha256'],
        '19a2b71362a020d560a6f67b5272da7b6ed93445986675b5b218752ece2227cb',
        '5137b25948fc02cbccf5cc171b71853842ccc9bbb799f7aafbfba87aee526885',
    ):
        expected['licenses/BUNDLED_TOOLS_CN.md'] = bundled_notice_hash
    else:
        expected['licenses/BUNDLED_TOOLS_CN.md'] = lock['bundled_notice_sha256']
    for entry in index:
        identity = entry['ecosystem']+':'+entry['name']+'@'+entry['version']
        key = hashlib.sha256(identity.encode()).hexdigest()[:24]
        if not isinstance(entry.get('notices'),list) or len(entry['notices'])>32:
            raise ValueError('Invalid dependency notice references')
        selected = {}
        for notice in entry['notices']:
            if set(notice) != {'path','sha256'}:
                raise ValueError('Unexpected notice fields')
            name = notice['path']
            if not re.fullmatch(r'licenses/dependencies/'+key+r'/[0-3]/(?:LICENSE|LICENCE|COPYING|NOTICE)[A-Za-z0-9._-]*',name,re.IGNORECASE) or name in expected:
                raise ValueError('Invalid or duplicate dependency notice path')
            selected[name] = notice['sha256']
            expected[name] = notice['sha256']
        rule = rules[key]
        required = set(rule['required'].items())
        optional = set(rule['optional'].items())
        if not required <= set(selected.items()) <= required | optional:
            raise ValueError('Missing or misattributed dependency notice for component: '+key)
    if {name:value for name,value in hashes.items() if name.startswith('licenses/')} != expected:
        raise ValueError('License subtree differs from trusted public notice inventory')


def inspect_package(archive, target, version, recipe, toolchain_pin):
    """Read/hash all entries without extraction; identities come from trusted checkout."""
    if archive.stat().st_size > 512 << 20:
        raise ValueError('Portable archive exceeds compressed size limit')
    binary = 'cliproxyapi-plus.exe' if target.startswith('windows/') else 'cliproxyapi-plus'
    fixed = {binary, 'build-info.json', 'checksums.txt', 'update-tools.json', 'update-tools.example.json', 'upstream.lock.json', 'README.md', 'THIRD_PARTY_NOTICES.md', 'compliance-report.json', 'licenses-index.json', 'sbom.spdx.json'}
    fixed.update({'licenses/cliproxyapi-LICENSE.txt', 'licenses/cpa-manager-plus-LICENSE.txt', 'licenses/BUNDLED_TOOLS_CN.md'})
    fixed.update('docs/' + name for name in DOCS)
    fixed.update('tools/' + name for name in TOOLS)
    hashes, modes, small, seen = {}, {}, {}, set()
    count = total = 0

    def inspect(name, size, mode, reader):
        nonlocal count, total
        archive_paths.validate(name)
        if name.casefold() in seen or stat.S_IFMT(mode) not in (0, stat.S_IFREG):
            raise ValueError('Duplicate or linked package entry')
        seen.add(name.casefold())
        if name not in fixed and not name.startswith(('build-tools/', 'updater-source/', 'licenses/')):
            raise ValueError('Unexpected package entry: ' + name)
        count += 1
        total += size
        if count > 50000 or total > 2 << 30 or size > 384 << 20:
            raise ValueError('Portable expanded content exceeds bounds')
        hashes[name] = hashlib.file_digest(reader, 'sha256').hexdigest()
        modes[name] = mode
        # Re-open only bounded metadata below. Never copy private runtime paths.

    if archive.name.endswith('.zip'):
        with zipfile.ZipFile(archive) as src:
            for item in src.infolist():
                if item.is_dir():
                    raise ValueError('Only regular package files are allowed')
                with src.open(item) as reader:
                    inspect(item.filename, item.file_size, item.external_attr >> 16, reader)
            for name in ('build-info.json', 'checksums.txt', 'update-tools.json', 'build-tools/toolchain-manifest.json', 'updater-source/source-kit.json', 'licenses-index.json'):
                if src.getinfo(name).file_size > 4 << 20:
                    raise ValueError('Oversized package metadata')
                small[name] = src.read(name)
    else:
        with tarfile.open(archive, 'r|gz') as src:
            for item in src:
                if not item.isfile():
                    raise ValueError('Only regular package files are allowed')
                with src.extractfile(item) as reader:
                    # These metadata sizes are bounded before materializing JSON.
                    if item.name in ('build-info.json', 'checksums.txt', 'update-tools.json', 'build-tools/toolchain-manifest.json', 'updater-source/source-kit.json', 'licenses-index.json'):
                        if item.size > 4 << 20:
                            raise ValueError('Oversized package metadata')
                        import io
                        small[item.name] = reader.read()
                        inspect(item.name, item.size, item.mode, io.BytesIO(small[item.name]))
                    else:
                        inspect(item.name, item.size, item.mode, reader)
    if not fixed.issubset(hashes):
        raise ValueError('Incomplete Portable root files')
    info = public_json(small['build-info.json'])
    kit = public_json(small['updater-source/source-kit.json'])
    sdk_raw = small['build-tools/toolchain-manifest.json']
    sdk = public_json(sdk_raw)
    if hashlib.sha256(sdk_raw).hexdigest() != toolchain_pin:
        raise ValueError('Bundled SDK manifest differs from trusted platform lock')
    sdk_hashes = {name.removeprefix('build-tools/'): value for name, value in hashes.items() if name.startswith('build-tools/') and name != 'build-tools/toolchain-manifest.json'}
    recipe_hashes = {name.removeprefix('updater-source/'): value for name, value in hashes.items() if name.startswith('updater-source/') and name != 'updater-source/source-kit.json'}
    if sdk.get('target') != target or sdk.get('records') != sdk_hashes:
        raise ValueError('Bundled SDK content mismatch')
    if kit != recipe or recipe_hashes != recipe.get('records'):
        raise ValueError('Source recipe differs from trusted checkout')
    if (info.get('target'), info.get('variant'), info.get('version'), info.get('product_source_sha256'), info.get('upstream_lock_sha256')) != (target, 'portable', version, recipe['product_source_sha256'], recipe['upstream_lock_sha256']):
        raise ValueError('Package build identity mismatch')
    if hashes[binary] != info.get('binary_sha256') or small['checksums.txt'].decode().strip() != hashes[binary] + '  ' + binary:
        raise ValueError('Program hash mismatch')
    if hashes['upstream.lock.json'] != recipe['upstream_lock_sha256']:
        raise ValueError('Packaged upstream lock mismatch')
    if public_json(small['update-tools.json']) != {'bundle': './build-tools', 'go_proxy': 'https://goproxy.cn|https://proxy.golang.org'}:
        raise ValueError('Bundled update tools default mismatch')
    for packaged, source in [('README.md','README.md'), ('THIRD_PARTY_NOTICES.md','THIRD_PARTY_NOTICES.md')] + [('docs/'+name,'docs/'+name) for name in DOCS] + [('tools/'+name,'scripts/'+name) for name in TOOLS]:
        if hashes[packaged] != recipe['records'].get(source):
            raise ValueError('Packaged documentation/tool drift: ' + packaged)
    verify_licenses(hashes,small['licenses-index.json'],recipe,target)
    if target.startswith('linux/'):
        executable = {binary} | {'build-tools/' + name for name in sdk['executables']}
        for name, mode in modes.items():
            if mode & 0o7777 != (0o755 if name in executable else 0o644):
                raise ValueError('Incorrect Linux package permissions: ' + name)
    return {'target': target, 'archive': archive.name, 'archive_sha256': digest(archive), 'binary_sha256': hashes[binary], 'product_source_sha256': recipe['product_source_sha256'], 'version': version, 'file_count': count, 'unpacked_bytes': total}


def native_target():
    machine = platform.machine().lower()
    arch = {'amd64':'amd64','x86_64':'amd64','aarch64':'arm64','arm64':'arm64'}.get(machine)
    return ('windows' if os.name == 'nt' else sys.platform) + '/' + str(arch)


def stage_native(target, commit, output):
    if target != native_target() or target not in TARGETS or not re.fullmatch('[0-9a-f]{40}', commit):
        raise ValueError('Portable validation must run natively at an exact source commit')
    version = (ROOT/'VERSION').read_text().strip()
    recipe = source_kit.manifest(ROOT)
    pins = public_json((ROOT/'internal/sourceupdate/toolchains.lock.json').read_text())
    archive = ROOT/'dist'/archive_name(version, target)
    report = inspect_package(archive, target, version, recipe, pins['platforms'][target]['manifest_sha256'])
    sidecar = archive.with_name(archive.name + '.sha256')
    if sidecar.read_text().strip() != report['archive_sha256'] + '  ' + archive.name:
        raise ValueError('Archive sidecar mismatch')
    checks = NATIVE_CHECKS
    for name in checks:
        value = public_json((ROOT/'.build'/name).read_text())
        if value.get('passed') is not True:
            raise ValueError('Missing native acceptance: ' + name)
    kit_report = public_json((ROOT/'.build/source-update-kit-report.json').read_text())
    if kit_report.get('product_source_sha256') != recipe['product_source_sha256']:
        raise ValueError('Stale source recipe acceptance')
    binary = ROOT/'dist'/(target.replace('/','-')+'-portable')/('cliproxyapi-plus.exe' if target.startswith('windows/') else 'cliproxyapi-plus')
    if digest(binary) != report['binary_sha256']:
        raise ValueError('Archive program differs from the smoke-tested executable')
    identity = public_json((ROOT/'.build/binary-smoke-report.json').read_text())['identity']
    # The binary smoke reads the freshly built executable, not an old manifest.
    if identity.get('product_source_sha256') != report['product_source_sha256'] or identity.get('version') != version or identity.get('os')+'/'+identity.get('arch') != target:
        raise ValueError('Stale native binary smoke')
    if output.exists():
        raise ValueError('Release staging directory must be new')
    output.mkdir(parents=True)
    shutil.copyfile(archive, output/archive.name)
    shutil.copyfile(sidecar, output/sidecar.name)
    report.update({'source_commit':commit, 'native_checks':checks, 'passed':True})
    (output/(target.replace('/','-')+'.json')).write_text(json.dumps(report,indent=2)+'\n')


def collect_assets(directory, version, commit, recipe):
    names = {archive_name(version, target) for target in TARGETS}
    expected = names | {name+'.sha256' for name in names} | {target.replace('/','-')+'.json' for target in TARGETS}
    if {file.name for file in directory.iterdir()} != expected or any(not file.is_file() or file.is_symlink() for file in directory.iterdir()):
        raise ValueError('Release requires exactly three native Portable archives, sidecars and receipts')
    pins = public_json((ROOT/'internal/sourceupdate/toolchains.lock.json').read_text())
    reports = []
    for target in TARGETS:
        archive = directory/archive_name(version,target)
        checked = inspect_package(archive,target,version,recipe,pins['platforms'][target]['manifest_sha256'])
        receipt = public_json((directory/(target.replace('/','-')+'.json')).read_text())
        if set(receipt) != set(checked) | {'source_commit','passed','native_checks'} or any(receipt.get(key) != value for key,value in checked.items()) or receipt.get('source_commit') != commit or receipt.get('passed') is not True or receipt.get('native_checks') != NATIVE_CHECKS:
            raise ValueError('Native receipt does not match release identity')
        if (directory/(archive.name+'.sha256')).read_text().strip() != checked['archive_sha256'] + '  ' + archive.name:
            raise ValueError('Archive sidecar mismatch')
        reports.append(receipt)
    return reports


def api(path):
    # Read-only preflight; tokens are never persisted, interpolated into URLs or printed.
    if not path.startswith('/repos/'+REPO+'/'):
        raise ValueError('API outside authorized repository')
    request = urllib.request.Request('https://api.github.com'+path, headers={'Authorization':'Bearer '+os.environ['GH_TOKEN'], 'Accept':'application/vnd.github+json', 'X-GitHub-Api-Version':'2022-11-28'})
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self,*args,**kwargs): return None
    with urllib.request.build_opener(NoRedirect()).open(request, timeout=60) as response:
        return json.load(response)


def preflight(tag, commit):
    version = release_version(tag)
    if os.environ.get('GITHUB_REPOSITORY') != REPO or os.environ.get('GITHUB_REF') != 'refs/heads/master':
        raise ValueError('Dispatch the release workflow from trusted master only')
    if not re.fullmatch('[0-9a-f]{40}',commit) or (ROOT/'VERSION').read_text().strip() != version:
        raise ValueError('Release version/commit mismatch')
    prefix = '/repos/'+REPO
    if api(prefix+'/commits/'+tag)['sha'] != commit or api(prefix+'/commits/master')['sha'] != commit:
        raise ValueError('Tag, workflow commit and trusted master must match')
    runs = api(prefix+'/actions/workflows/verify.yml/runs?head_sha='+commit+'&per_page=100')['workflow_runs']
    green = [run for run in runs if run.get('head_sha')==commit and run.get('head_branch') in ('master', tag, 'refs/tags/'+tag) and run.get('event') in ('push', 'workflow_dispatch') and run.get('conclusion')=='success']
    if not green:
        raise ValueError('Same-commit master verification must succeed before Portable publication')
    print('Release preflight passed:', tag, commit)


def missing_assets(files, existing):
    present = {}
    for asset in existing:
        name = asset['name']
        if name in present or name not in files or asset.get('digest') != 'sha256:'+files[name]:
            raise ValueError('Existing release asset differs; refusing overwrite: '+name)
        present[name] = asset
    return sorted(set(files)-set(present))


def gh(*args):
    return subprocess.check_output(['gh',*args,'--repo',REPO], text=True)


def publish(tag, commit, directory):
    preflight(tag,commit)
    version = release_version(tag)
    reports = collect_assets(directory,version,commit,source_kit.manifest(ROOT))
    # Receipts are intentionally public-safe; raw .build reports/logs never leave the runner.
    for report in reports:
        (directory/(report['target'].replace('/','-')+'.json')).write_text(json.dumps(report,indent=2,sort_keys=True)+'\n',encoding='utf-8',newline='\n')
    manifest = {'version':version,'source_commit':commit,'platforms':reports,'production_acceptance_claimed':False}
    (directory/'release-manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
    (directory/'SHA256SUMS').write_text(''.join(digest(directory/name)+'  '+name+'\n' for name in sorted(archive_name(version,t) for t in TARGETS)))
    files = {file.name:digest(file) for file in directory.iterdir()}
    # gh performs authenticated asset transfer; no credential forwarding implementation here.
    listed = public_json(gh('release','list','--limit','100','--json','tagName,isDraft'))
    exists = next((value for value in listed if value['tagName']==tag),None)
    if exists is None:
        gh('release','create',tag,'--draft','--verify-tag','--title','CLIProxyAPIPlus '+tag,'--notes-file',str(ROOT/'docs/releases'/('v'+version+'.md')))
    value = public_json(gh('release','view',tag,'--json','databaseId,isDraft'))
    release_id = value.get('databaseId')
    if not isinstance(release_id,int) or isinstance(release_id,bool) or release_id<=0:
        raise ValueError('GitHub did not return a valid release ID')
    # gh view lacks the REST digest; query via read-only API for authoritative asset SHA256.
    release = api('/repos/'+REPO+'/releases/'+str(release_id))
    missing = missing_assets(files,release['assets'])
    if missing and not value['isDraft']:
        raise ValueError('Published releases are immutable; create a new version')
    for name in missing:
        gh('release','upload',tag,str(directory/name))
    release = api('/repos/'+REPO+'/releases/'+str(release_id))
    if missing_assets(files,release['assets']):
        raise ValueError('Release attachments incomplete; keeping draft')
    preflight(tag,commit)
    if value['isDraft']:
        gh('release','edit',tag,'--draft=false','--latest')
    print('Published all three Portable packages:',tag)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command',choices=['preflight','stage','publish'])
    parser.add_argument('--tag',default=os.environ.get('RELEASE_TAG',''))
    parser.add_argument('--commit',default=os.environ.get('GITHUB_SHA',''))
    parser.add_argument('--target',choices=TARGETS)
    parser.add_argument('--directory',type=Path,default=ROOT/'.build/release-assets')
    args=parser.parse_args()
    if args.command=='preflight': preflight(args.tag,args.commit)
    elif args.command=='stage': stage_native(args.target,args.commit,args.directory)
    else: publish(args.tag,args.commit,args.directory)

if __name__=='__main__':
    try: main()
    except (OSError,ValueError,KeyError,subprocess.CalledProcessError,zipfile.BadZipFile,tarfile.TarError) as error:
        # Public errors contain source/asset names only, never raw runner logs or headers.
        print('Portable release failed:',str(error),file=sys.stderr)
        sys.exit(1)
