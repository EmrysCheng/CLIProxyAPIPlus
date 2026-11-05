#!/usr/bin/env python3
"""Stage, validate and transactionally promote a dual-upstream source candidate."""
from __future__ import annotations
from contextlib import contextmanager
import archive_paths
import argparse
import datetime
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import sys
import urllib.parse
import urllib.request
import uuid
import zipfile
import project
from build_progress import step

ROOT=project.ROOT
PRODUCT_PATHS=['cmd','internal','patches','scripts','tests','go.mod','VERSION','.github','Dockerfile','compose.yaml','.dockerignore','.gitattributes','.gitignore','AGENTS.md','README_CN.md','THIRD_PARTY_NOTICES.md','docs']

def read_json(path):return json.loads(path.read_text(encoding='utf-8'))
def save_json(path,data):path.parent.mkdir(parents=True,exist_ok=True);path.write_text(json.dumps(data,indent=2)+'\n',encoding='utf-8')
def sha(path):return hashlib.sha256(path.read_bytes()).hexdigest()
def product_digest(root):
    records={}
    for name in PRODUCT_PATHS:
        path=root/name
        files=[path] if path.is_file() else list(path.rglob('*')) if path.exists() else []
        for file in files:
            if file.is_file() and '__pycache__' not in file.parts:
                if file.is_symlink():raise ValueError('Product source symlinks are not supported')
                records[file.relative_to(root).as_posix()]=sha(file)
    return hashlib.sha256(json.dumps(records,sort_keys=True).encode()).hexdigest()

def safe_extract(data:bytes,target:Path):
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        items=archive.infolist()
        if not items or len(items)>30000 or sum(item.file_size for item in items)>512*1024*1024:raise ValueError('Source archive exceeds limits')
        prefixes=set()
        for item in items:
            name=item.filename
            archive_paths.validate(name,item.is_dir())
            path=PurePosixPath(name)
            if '\\' in name or ':' in name or path.is_absolute() or any(part in ['..','.git'] for part in path.parts):raise ValueError('Unsafe archive entry')
            if stat.S_ISLNK(item.external_attr>>16):raise ValueError('Archive symlinks are forbidden')
            if not path.parts:continue
            prefixes.add(path.parts[0])
        if len(prefixes)!=1:raise ValueError('Expected a single repository archive root')
        for item in items:
            parts=PurePosixPath(item.filename).parts[1:]
            if not parts:continue
            destination=project.child(target,'/'.join(parts))
            if item.is_dir():destination.mkdir(parents=True,exist_ok=True)
            else:
                destination.parent.mkdir(parents=True,exist_ok=True)
                with archive.open(item) as src,destination.open('xb') as out:shutil.copyfileobj(src,out)

class _GithubRedirect(urllib.request.HTTPRedirectHandler):
    max_repeats=2
    max_redirections=3
    def redirect_request(self, request, fp, code, message, headers, newurl):
        initial=urllib.parse.urlparse(request.full_url);target=urllib.parse.urlparse(newurl)
        if target.scheme!='https' or target.hostname!=initial.hostname or target.hostname not in ['api.github.com','codeload.github.com'] or target.username is not None or target.password is not None or target.fragment:
            raise ValueError('GitHub source redirect escaped its fixed trusted HTTPS origin')
        return super().redirect_request(request,fp,code,message,headers,newurl)

def open_github(request,timeout):
    target=urllib.parse.urlparse(request.full_url)
    if target.scheme!='https' or target.hostname not in ['api.github.com','codeload.github.com'] or target.username is not None or target.password is not None:
        raise ValueError('Untrusted GitHub source destination')
    return urllib.request.build_opener(_GithubRedirect()).open(request,timeout=timeout)

def github(repo,suffix):
    req=urllib.request.Request('https://api.github.com/repos/'+repo+suffix,headers={'Accept':'application/vnd.github+json','User-Agent':'CLIProxyAPIPlus-source-updater'})
    token=os.environ.get('GH_TOKEN') or os.environ.get('GITHUB_TOKEN')
    if token:req.add_header('Authorization','Bearer '+token)
    with open_github(req,timeout=30) as response:return json.load(response)

def stage(args):
    with promotion_lock():return _stage(args)

def _stage(args):
    if args.ref and args.project=="all":raise ValueError("Pinned refs require --project; the two repositories do not share tag names")
    locked=project.lockfile();candidate=ROOT/'.build/candidates'/uuid.uuid4().hex;source=candidate/'source';source.mkdir(parents=True)
    for name in PRODUCT_PATHS:
        old=ROOT/name;new=source/name
        if not old.exists():continue
        if old.is_dir():shutil.copytree(old,new,ignore=shutil.ignore_patterns('__pycache__'))
        else:shutil.copyfile(old,new)
    changed=[]
    for name,item in locked['projects'].items():
        pins=getattr(args,'pins',None)
        if (pins is not None and name not in pins) or args.project not in ['all',name]:
            shutil.copytree(ROOT/'upstream'/name,source/'upstream'/name);continue
        if args.offline:
            ref=item['ref'];commit=item['commit'];shutil.copytree(ROOT/'upstream'/name,source/'upstream'/name)
        else:
            ref=pins[name]['version'] if pins is not None else (args.ref or (github(item['repository'],'/releases/latest')['tag_name'] if args.channel=='stable' else 'main'))
            commit=pins[name]['commit'] if pins is not None else github(item['repository'],'/commits/'+urllib.parse.quote(ref,safe=''))['sha']
            if not re.fullmatch('[0-9a-f]{40}',commit):raise ValueError('Invalid upstream commit identity')
            request=urllib.request.Request('https://codeload.github.com/'+item['repository']+'/zip/'+commit,headers={'User-Agent':'CLIProxyAPIPlus-source-updater'})
            with open_github(request,timeout=90) as response:data=response.read(128*1024*1024+1)
            if len(data)>128*1024*1024:raise ValueError('Compressed source exceeds size limit')
            destination=source/'upstream'/name;destination.mkdir(parents=True);safe_extract(data,destination)
        destination=source/'upstream'/name
        if not (destination/'LICENSE').is_file():raise ValueError('Upstream license is missing')
        module=destination/('go.mod' if name=='cliproxyapi' else 'apps/manager-server/go.mod')
        expected='github.com/router-for-me/CLIProxyAPI/v8' if name=='cliproxyapi' else 'github.com/seakee/cpa-manager-plus/apps/manager-server'
        if not module.read_text(encoding='utf-8').startswith('module '+expected+'\n'):raise ValueError('Upstream module identity changed; manual adapter review is required')
        digest=project.tree_digest(destination)
        if commit!=item['commit'] or digest!=item['tree_sha256']:changed.append(name)
        item.update(ref=ref,commit=commit,tree_sha256=digest)
    locked['channel']=args.channel;locked['checked_at']=datetime.datetime.now(datetime.timezone.utc).isoformat()
    save_json(source/'upstream.lock.json',locked)
    metadata={'schema_version':1,'candidate':str(candidate),'source':str(source),'base_lock_sha256':sha(ROOT/'upstream.lock.json'),'product_digest':product_digest(ROOT),'changed':changed,'channel':args.channel}
    save_json(candidate/'candidate.json',metadata)
    print(json.dumps(metadata,indent=2))
    return candidate

def candidate_path(raw):
    path=Path(raw).resolve();allowed=(ROOT/'.build/candidates').resolve()
    if not path.is_relative_to(allowed) or path==allowed:raise ValueError('Candidate must be inside this workspace .build/candidates')
    return path

def validate(args):
    candidate=candidate_path(args.candidate);meta=read_json(candidate/'candidate.json');source=candidate/'source'
    if product_digest(source)!=meta['product_digest']:raise ValueError('Candidate product code changed since staging')
    lock=read_json(source/'upstream.lock.json')
    for name,item in lock['projects'].items():
        if project.tree_digest(source/'upstream'/name)!=item['tree_sha256']:raise ValueError('Candidate source drift')
    env=os.environ.copy()
    for name in list(env):
        if any(term in name.upper() for term in ['TOKEN','SECRET','PASSWORD','API_KEY']) or name.upper().startswith(('AWS_','AZURE_','GOOGLE_','PGSTORE_','GITSTORE_','OBJECTSTORE_')):env.pop(name,None)
    commands=[['build'],['test-e2e'],['test-update'],['test-source'],['test-web']]
    # The independent standalone/worker regressions also run without secrets.
    receipt={'schema_version':1,'passed':False,'candidate_lock_sha256':sha(source/'upstream.lock.json'),'product_digest':meta['product_digest'],'commands':[]}
    save_json(candidate/'validation.json',receipt)
    for command in commands:
        if command[0]!='build':step({'test-e2e':'single_port_test','test-update':'update_test','test-source':'recipe_test','test-web':'frontend_tests'}[command[0]])
        cmd=[sys.executable,str(source/'scripts/project.py'),*command,'--go',args.go]
        if args.node:cmd+=['--node',str(Path(args.node).resolve())]
        if args.npm_cli:cmd+=['--npm-cli',str(Path(args.npm_cli).resolve())]
        if getattr(args, 'tool_bundle', None):cmd+=['--tool-bundle',str(Path(args.tool_bundle).resolve())]
        subprocess.run(cmd,cwd=source,env=env,check=True)
        receipt['commands'].append(command[0]);save_json(candidate/'validation.json',receipt)
    step('adapter_tests')
    subprocess.run([sys.executable,str(source/'scripts/regression.py'),'--go',args.go],cwd=source,env=env,check=True)
    receipt['commands'].append('regression')
    for script in ['smoke_binary.py','smoke_maintenance.py']:
        step('binary_smoke' if script=='smoke_binary.py' else 'maintenance_smoke')
        subprocess.run([sys.executable,str(source/'scripts'/script)],cwd=source,env=env,check=True)
        receipt['commands'].append(script)
    if product_digest(source)!=meta['product_digest']:raise ValueError('Validation modified product source')
    receipt['passed']=True;receipt['completed_at']=datetime.datetime.now(datetime.timezone.utc).isoformat();save_json(candidate/'validation.json',receipt)
    print('Validated candidate:',candidate)

def checked_workspace_path(path):
    resolved=path.resolve()
    if not resolved.is_relative_to(ROOT.resolve()) or resolved==ROOT.resolve():raise ValueError('Promotion path escapes workspace')
    return resolved

def recover_journal(path):
    journal=read_json(path);folder=checked_workspace_path(path.parent)
    if folder.parent != (ROOT/'.build/promotions').resolve():raise ValueError('Invalid promotion journal location')
    for name in reversed(journal['moved']):
        if name not in ['cliproxyapi','cpa-manager-plus']:raise ValueError('Invalid journal project')
        current=checked_workspace_path(ROOT/'upstream'/name);old=checked_workspace_path(folder/('old-'+name))
        if old.exists():
            if current.exists():current.rename(checked_workspace_path(folder/('failed-'+name)))
            old.rename(current)
    oldlock=folder/'old-lock.json'
    if oldlock.exists():os.replace(oldlock,ROOT/'upstream.lock.json')
    journal['state']='rolled_back';save_json(path,journal)

def promotion_lock():
    from workspace_lock import file_lock
    return file_lock(ROOT/'.build/promotion.lock')

def promote(args):
    with promotion_lock():return _promote(args)

def _promote(args):
    candidate=candidate_path(args.candidate);meta=read_json(candidate/'candidate.json');receipt=read_json(candidate/'validation.json');source=candidate/'source'
    if not receipt.get('passed') or receipt['candidate_lock_sha256']!=sha(source/'upstream.lock.json'):raise ValueError('Candidate has no matching successful validation receipt')
    if product_digest(ROOT)!=meta['product_digest'] or product_digest(source)!=meta['product_digest']:raise ValueError('Product/adapter changed; restage and revalidate')
    if sha(ROOT/'upstream.lock.json')!=meta['base_lock_sha256']:raise ValueError('Stable lock changed while candidate was being validated')
    lock=read_json(source/'upstream.lock.json')
    folder=ROOT/'.build/promotions'/uuid.uuid4().hex;folder.mkdir(parents=True)
    for name,item in lock['projects'].items():
        if project.tree_digest(source/'upstream'/name)!=item['tree_sha256']:raise ValueError('Candidate upstream changed after validation')
        shutil.copytree(source/'upstream'/name,folder/('new-'+name))
    shutil.copyfile(ROOT/'upstream.lock.json',folder/'old-lock.json')
    journal={'schema_version':1,'state':'prepared','candidate':str(candidate),'moved':[]};journal_path=folder/'journal.json';save_json(journal_path,journal)
    try:
        for name in lock['projects']:
            old=checked_workspace_path(ROOT/'upstream'/name);backup=checked_workspace_path(folder/('old-'+name));incoming=checked_workspace_path(folder/('new-'+name))
            # Journal the intent before the first rename so recovery covers process interruption.
            journal['moved'].append(name);save_json(journal_path,journal)
            old.rename(backup);incoming.rename(old)
        pending=folder/'new-lock.json';shutil.copyfile(source/'upstream.lock.json',pending);os.replace(pending,ROOT/'upstream.lock.json')
        journal['state']='complete';save_json(journal_path,journal)
    except BaseException:
        recover_journal(journal_path);raise
    print('Promoted verified source combination. Recovery journal:',journal_path)

def main():
    parser=argparse.ArgumentParser(description=__doc__);sub=parser.add_subparsers(dest='command',required=True)
    p=sub.add_parser('stage');p.add_argument('--project',choices=['all','cliproxyapi','cpa-manager-plus'],default='all');p.add_argument('--channel',choices=['stable','edge'],default='stable');p.add_argument('--ref');p.add_argument('--offline',action='store_true')
    for name in ['validate','promote']:
        p=sub.add_parser(name);p.add_argument('--candidate',required=True)
        if name=='validate':p.add_argument('--go',default='go');p.add_argument('--node');p.add_argument('--npm-cli');p.add_argument('--tool-bundle')
    p=sub.add_parser('recover');p.add_argument('--journal',required=True)
    args=parser.parse_args()
    if args.command=='stage':stage(args)
    elif args.command=='validate':validate(args)
    elif args.command=='promote':promote(args)
    else:
        with promotion_lock():recover_journal(Path(args.journal).resolve())
if __name__=='__main__':
    try:main()
    except (ValueError,OSError,subprocess.CalledProcessError) as err:print('ERROR:',err,file=sys.stderr);sys.exit(1)