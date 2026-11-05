#!/usr/bin/env python3
"""Offline binary upgrade with paired data snapshot and explicit rollback."""
import private
import argparse
from contextlib import contextmanager, nullcontext
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import urllib.request
import uuid
import data as maintenance

@contextmanager
def marker(data_dir):
    path=Path(str(data_dir.resolve())+'.maintenance');nonce=uuid.uuid4().hex
    with path.open('x',encoding='utf-8') as file:file.write(nonce)
    try:yield nonce
    finally:
        if path.exists() and path.read_text(encoding='utf-8')==nonce:path.unlink()

def save(path,obj):path.write_text(json.dumps(obj,indent=2)+'\n',encoding='utf-8')
def verify_plan(args):
    binary=args.binary.resolve();candidate=args.candidate.resolve();config=args.config.resolve();data=args.data_dir.resolve()
    if not binary.is_file() or not candidate.is_file() or not config.is_file() or not data.is_dir():raise ValueError('Current binary, candidate, config and data must exist')
    if len(args.sha256)!=64 or maintenance.sha(candidate)!=args.sha256.lower():raise ValueError('Candidate checksum does not match the trusted release checksum')
    if any(path.is_symlink() for path in [args.binary,args.candidate,args.config,args.data_dir]):raise ValueError('Upgrade paths cannot be symlinks')
    return binary,candidate,config,data

def restore_requires_relocation(manifest,data):
    inspection=manifest.get('inspection',{})
    auth=Path(inspection.get('auth_dir') or data/'auths').resolve()
    if auth!=(data/'auths').resolve():return True
    if inspection.get('tls_enabled'):
        return any(Path(inspection.get(field,'')).resolve()!=(data/'tls'/name).resolve() for field,name in [('tls_cert','server.crt'),('tls_key','server.key')])
    return False

def rollback(journal_path,already_marked=False):
    record=json.loads(journal_path.read_text(encoding='utf-8'));folder=journal_path.parent.resolve()
    binary=Path(record['binary']).resolve();config=Path(record['config']).resolve();data=Path(record['data']).resolve()
    if folder!=Path(record['state']).resolve() or binary.parent!=Path(record['installation_dir']).resolve():raise ValueError('Upgrade journal identity mismatch')
    old=folder/'previous-binary';snapshot=folder/'data-snapshot.zip'
    if maintenance.sha(old)!=record['previous_binary_sha256'] or maintenance.sha(snapshot)!=record['snapshot_sha256']:raise ValueError('Rollback backup checksum mismatch')
    manifest=maintenance.verify(snapshot)
    marker_path=Path(str(data)+'.maintenance')
    recovery_marker=marker_path.exists() and marker_path.read_text(encoding='utf-8')==record.get('maintenance_marker')
    if marker_path.exists() and not already_marked and not recovery_marker:raise ValueError('Unrelated maintenance operation is active')
    with nullcontext() if already_marked or recovery_marker else marker(data):
        if data.exists():
            with maintenance.database_lock(data):pass
            failed=data.parent/(data.name+'.failed-'+uuid.uuid4().hex)
            if failed.parent.resolve()!=data.parent.resolve():raise ValueError('Invalid failed-data target')
            data.rename(failed);record['failed_data']=str(failed)
        if config.exists():
            failed_config=config.parent/(config.name+'.failed-'+uuid.uuid4().hex);config.rename(failed_config);record['failed_config']=str(failed_config)
        incoming=binary.with_name(binary.name+'.rollback-'+uuid.uuid4().hex);shutil.copyfile(old,incoming);os.chmod(incoming,0o755);os.replace(incoming,binary)
        maintenance.restore(snapshot,data,config,binary,relocate=restore_requires_relocation(manifest,data))
        record['state_status']='rolled_back';save(journal_path,record)
    if recovery_marker and not already_marked and marker_path.exists():marker_path.unlink()
    return record

def install(args):
    binary,candidate,config,data=verify_plan(args)
    plan={'binary':str(binary),'candidate':str(candidate),'config':str(config),'data':str(data),'candidate_sha256':args.sha256,'requires_stopped_service':True,'applied':False}
    if not args.apply:return plan
    state=args.state_dir.resolve()/uuid.uuid4().hex;state.mkdir(parents=True,mode=0o700);private.protect(state,True)
    if state.is_relative_to(data):raise ValueError('Upgrade history must not be inside the data directory')
    with marker(data) as marker_nonce:
        with maintenance.database_lock(data):
            inspection=json.loads(maintenance.run_binary(binary,['--inspect-config','--config',config]))
            # The maintenance marker prevents restart during the release/reacquire gap.
            shutil.copyfile(binary,state/'previous-binary')
        snapshot=state/'data-snapshot.zip';maintenance.snapshot(config,data,snapshot,inspection)
        record={**plan,'schema_version':1,'maintenance_marker':marker_nonce,'state':str(state),'installation_dir':str(binary.parent),'previous_binary_sha256':maintenance.sha(state/'previous-binary'),'snapshot_sha256':maintenance.sha(snapshot),'state_status':'prepared'}
        journal=state/'journal.json';save(journal,record)
        changed=False
        try:
            staged=binary.with_name(binary.name+'.candidate-'+uuid.uuid4().hex);shutil.copyfile(candidate,staged);os.chmod(staged,0o755)
            if maintenance.sha(staged)!=args.sha256.lower():raise ValueError('Candidate changed during copy')
            record['state_status']='installing';save(journal,record);os.replace(staged,binary);changed=True
            maintenance.run_binary(binary,['--check-data','--data-dir',data])
            if args.simulate_failure:raise RuntimeError('Injected post-migration failure for rollback verification')
            record.update(applied=True,state_status='awaiting_restart_and_health_check',journal=str(journal));save(journal,record)
            return record
        except BaseException:
            if changed:rollback(journal,already_marked=True)
            raise

def check_running(url,version):
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for suffix in ['/plus-api/ready','/plus-api/version']:
        with opener.open(url.rstrip('/')+suffix,timeout=10) as response:payload=json.load(response)
        if suffix.endswith('ready') and payload.get('ready') is not True:raise ValueError('Service is not ready')
        if suffix.endswith('version') and version and payload.get('version')!=version:raise ValueError('Running version does not match the intended release')
    return {'healthy':True,'base_url':url,'expected_version':version}

def main():
    parser=argparse.ArgumentParser(description=__doc__);sub=parser.add_subparsers(dest='command',required=True)
    p=sub.add_parser('install');p.add_argument('--binary',type=Path,required=True);p.add_argument('--candidate',type=Path,required=True);p.add_argument('--sha256',required=True);p.add_argument('--config',type=Path,required=True);p.add_argument('--data-dir',type=Path,required=True);p.add_argument('--state-dir',type=Path,required=True);p.add_argument('--apply',action='store_true');p.add_argument('--simulate-failure',action='store_true',help=argparse.SUPPRESS)
    p=sub.add_parser('rollback');p.add_argument('--journal',type=Path,required=True)
    p=sub.add_parser('verify-running');p.add_argument('--url',required=True);p.add_argument('--version',default='')
    args=parser.parse_args()
    if args.command=='install':result=install(args)
    elif args.command=='rollback':result=rollback(args.journal.resolve())
    else:result=check_running(args.url,args.version)
    print(json.dumps(result,indent=2))
if __name__=='__main__':
    try:main()
    except (OSError,ValueError,RuntimeError,subprocess.CalledProcessError) as error:print('ERROR:',error,file=sys.stderr);sys.exit(1)