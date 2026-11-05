#!/usr/bin/env python3
"""Offline, integrity-checked backup/import/restore for the integrated application."""
from __future__ import annotations
import platform
import archive_paths
import private
import argparse
from contextlib import contextmanager, closing
import datetime
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import sqlite3
import subprocess
import sys
import uuid
import zipfile

ROOT=Path(__file__).resolve().parents[1]
def default_binary(root=ROOT, *, system=None, machine=None):
    system = os.name if system is None else system
    executable = 'cliproxyapi-plus.exe' if system == 'nt' else 'cliproxyapi-plus'
    installed = root / executable
    if installed.is_file():
        return installed
    arch = 'arm64' if (machine or platform.machine()).lower() in ('aarch64', 'arm64') else 'amd64'
    target = 'windows-amd64' if system == 'nt' else 'linux-' + arch
    return root / 'dist' / (target + '-portable') / executable


DEFAULT_BINARY = default_binary()

def sha(path):
    digest=hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda:stream.read(1024*1024),b''):digest.update(block)
    return digest.hexdigest()

def run_binary(binary,args):
    result=subprocess.run([str(Path(binary).resolve()),*map(str,args)],capture_output=True,text=True,check=True,creationflags=subprocess.CREATE_NO_WINDOW if os.name=='nt' else 0)
    return result.stdout

@contextmanager
def database_lock(data_dir:Path,db_name='usage.sqlite'):
    data_dir=data_dir.resolve()
    if not data_dir.is_dir():raise ValueError('Data directory does not exist')
    db=data_dir/db_name
    if db.is_symlink() or (db.exists() and db.stat().st_nlink>1):raise ValueError('Database links are not supported for maintenance')
    lock=Path(str(db)+'.manager.lock')
    if lock.is_symlink() or (lock.exists() and lock.stat().st_nlink>1):raise ValueError('Database lock links are forbidden')
    file=lock.open('a+b');acquired=False
    try:
        file.seek(0)
        try:
            if os.name=='nt':
                import msvcrt
                msvcrt.locking(file.fileno(),msvcrt.LK_NBLCK,1)
            else:
                import fcntl
                fcntl.flock(file.fileno(),fcntl.LOCK_EX|fcntl.LOCK_NB)
            acquired=True
        except OSError as error:raise ValueError('Stop the application before maintenance; Manager database is locked') from error
        yield
    finally:
        if acquired:
            file.seek(0)
            if os.name=='nt':
                import msvcrt
                msvcrt.locking(file.fileno(),msvcrt.LK_UNLCK,1)
            else:
                import fcntl
                fcntl.flock(file.fileno(),fcntl.LOCK_UN)
        file.close()

def regular_files(root):
    if root.is_symlink():raise ValueError('Maintenance input cannot be a symlink')
    for file in sorted(root.rglob('*')):
        if file.is_symlink():raise ValueError('Snapshot refuses symlink: '+str(file))
        if file.is_file():yield file

def backup_database(source:Path,destination:Path):
    if not source.exists():return
    destination.parent.mkdir(parents=True,exist_ok=True)
    uri=source.resolve().as_uri()+'?mode=ro'
    with closing(sqlite3.connect(uri,uri=True)) as incoming,closing(sqlite3.connect(destination)) as outgoing:
        incoming.backup(outgoing)
        if outgoing.execute('PRAGMA quick_check').fetchone()[0]!='ok':raise ValueError('SQLite backup failed quick_check')

def snapshot(config:Path,data:Path,output:Path,inspection:dict):
    config=config.resolve();data=data.resolve();output=output.resolve()
    if output.exists():raise ValueError('Refusing to overwrite a backup')
    if output.is_relative_to(data):raise ValueError('Backup output must be outside the data directory')
    output.parent.mkdir(parents=True,exist_ok=True)
    stage=output.parent/('.snapshot-'+uuid.uuid4().hex);stage.mkdir(mode=0o700);private.protect(stage,True)
    with database_lock(data):
        before=sha(config)
        entries={}
        (stage/'data').mkdir()
        for file in regular_files(data):
            rel=file.relative_to(data)
            if rel.parts[0] in ['auths','tls'] or file.name.endswith(('.manager.lock','.sqlite-wal','.sqlite-shm')) or rel.as_posix()=='usage.sqlite':continue
            target=stage/'data'/rel;target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(file,target)
            if sha(file)!=sha(target):raise ValueError('Data changed while snapshotting')
        backup_database(data/'usage.sqlite',stage/'data/usage.sqlite')
        auth=Path(inspection['auth_dir']) if inspection.get('auth_dir') else data/'auths'
        if auth.exists():
            for file in regular_files(auth):
                target=stage/'data/auths'/file.relative_to(auth);target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(file,target)
                if sha(file)!=sha(target):raise ValueError('Credential changed during snapshot')
        if inspection.get('tls_enabled'):
            for field,name in [('tls_cert','server.crt'),('tls_key','server.key')]:
                file=Path(inspection[field]);target=stage/'data/tls'/name;target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(file,target)
        shutil.copyfile(config,stage/'config.yaml')
        if sha(config)!=before:raise ValueError('Configuration changed during snapshot')
        for file in regular_files(stage):entries[file.relative_to(stage).as_posix()]={'sha256':sha(file),'bytes':file.stat().st_size}
        manifest={'schema_version':1,'created_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'original_data':str(data),'original_config':str(config),'files':entries,'inspection':inspection}
        (stage/'manifest.json').write_text(json.dumps(manifest,indent=2),encoding='utf-8')
        payload=list(regular_files(stage))
        temporary=stage/'snapshot.zip'
        with zipfile.ZipFile(temporary,'x',zipfile.ZIP_DEFLATED) as archive:
            for file in payload:archive.write(file,file.relative_to(stage).as_posix())
        private.protect(temporary);os.replace(temporary,output);private.protect(output)
        output.with_name(output.name+'.sha256').write_text(sha(output)+'  '+output.name+'\n',encoding='utf-8')
    return manifest

def verify(snapshot_path:Path):
    with zipfile.ZipFile(snapshot_path) as archive:
        infos=archive.infolist();names=[item.filename for item in infos]
        if len(names)!=len(set(n.casefold() for n in names)) or len(names)>100000 or sum(i.file_size for i in infos)>8*1024**3:raise ValueError('Snapshot exceeds limits or has duplicate names')
        for item in infos:
            archive_paths.validate(item.filename,item.is_dir())
            path=PurePosixPath(item.filename)
            if path.is_absolute() or '\\' in item.filename or ':' in item.filename or '..' in path.parts or (item.external_attr>>16)&0o170000==0o120000:raise ValueError('Unsafe snapshot path')
        if archive.getinfo('manifest.json').file_size>8*1024*1024:raise ValueError('Snapshot manifest exceeds limit')
        manifest=json.loads(archive.read('manifest.json'))
        if not isinstance(manifest,dict) or manifest.get('schema_version')!=1 or not isinstance(manifest.get('files'),dict):raise ValueError('Unsupported snapshot schema')
        if any(name!='config.yaml' and not name.startswith('data/') for name in manifest['files']):raise ValueError('Snapshot contains an unexpected root path')
        if set(names)!=set(manifest['files'])|{'manifest.json'}:raise ValueError('Snapshot inventory mismatch')
        for name,record in manifest['files'].items():
            digest=hashlib.sha256();size=0
            with archive.open(name) as stream:
                for block in iter(lambda:stream.read(1024*1024),b''):digest.update(block);size+=len(block)
            if digest.hexdigest()!=record['sha256'] or size!=record['bytes']:raise ValueError('Snapshot checksum mismatch: '+name)
        return manifest

def restore(snapshot_path:Path,data:Path,config:Path,binary:Path,relocate=True):
    manifest=verify(snapshot_path);data=data.resolve();config=config.resolve()
    if data.exists() or config.exists():raise ValueError('Restore requires new data and configuration paths; existing data is never overwritten')
    data.parent.mkdir(parents=True,exist_ok=True);config.parent.mkdir(parents=True,exist_ok=True)
    stage=data.parent/('.restore-'+uuid.uuid4().hex);stage.mkdir(mode=0o700);private.protect(stage,True)
    with zipfile.ZipFile(snapshot_path) as archive:
        for name in manifest['files']:
            destination=stage/PurePosixPath(name)
            destination.parent.mkdir(parents=True,exist_ok=True)
            with archive.open(name) as src,destination.open('xb') as dst:shutil.copyfileobj(src,dst)
            os.chmod(destination,0o600)
    # Publish only verified data. The native helper preserves YAML layout and secrets.
    (stage/'data').rename(data)
    private_config=config.parent/('.restore-config-'+uuid.uuid4().hex);private_config.mkdir(mode=0o700);private.protect(private_config,True)
    incoming=private_config/'config.yaml';shutil.copyfile(stage/'config.yaml',incoming)
    with config.open('xb'):pass
    os.replace(incoming,config)
    private.protect(config)
    if relocate:run_binary(binary,['--relocate-config','--config',config,'--data-dir',data])
    return manifest

def import_data(binary,native,auth,manager_data,destination,listen):
    if not native.is_file() or not auth.is_dir():raise ValueError('Native configuration and authentication directory must exist')
    destination=destination.resolve()
    if destination.exists():raise ValueError('Import destination must be new')
    destination.mkdir(parents=True,mode=0o700);private.protect(destination,True)
    data=destination/'data';config=destination/'config.yaml'
    manager_data=manager_data.resolve() if manager_data else None
    lock=database_lock(manager_data) if manager_data else None
    if manager_data and (manager_data/'usage.sqlite').exists() and not (manager_data/'data.key').is_file():raise ValueError('Original Manager data.key is required; refusing to generate an unrelated encryption key')
    if lock:lock.__enter__()
    try:
        args=['--adopt-config',native,'--config',config,'--data-dir',data]
        if listen:args+=['--listen',listen]
        original_hash=sha(native)
        run_binary(binary,args)
        if auth and auth.exists():shutil.copytree(auth,data/'auths')
        if manager_data:
            backup_database(manager_data/'usage.sqlite',data/'usage.sqlite')
            for file in regular_files(manager_data):
                rel=file.relative_to(manager_data)
                if rel.parts[0]=='auths' or rel.as_posix() in ['admin.key','api.key','core-management.key','usage.sqlite'] or file.name.endswith(('.manager.lock','.sqlite-wal','.sqlite-shm')):continue
                target=data/rel;target.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(file,target)
            if (data/'usage.sqlite').exists():run_binary(binary,['--reset-admin','--data-dir',data])
        if sha(native)!=original_hash:raise ValueError('Native source configuration unexpectedly changed')
        report={'schema_version':1,'source_config_sha256':original_hash,'configuration':str(config),'data_dir':str(data),'source_preserved':True}
        (destination/'import-report.json').write_text(json.dumps(report,indent=2),encoding='utf-8')
        return report
    finally:
        if lock:lock.__exit__(None,None,None)

def main():
    parser=argparse.ArgumentParser(description=__doc__);sub=parser.add_subparsers(dest='command',required=True)
    for cmd in ['backup','restore']:
        p=sub.add_parser(cmd);p.add_argument('--binary',type=Path,default=DEFAULT_BINARY);p.add_argument('--data-dir',type=Path,required=True);p.add_argument('--config',type=Path,required=True);p.add_argument('--snapshot',type=Path,required=True)
    p=sub.add_parser('verify');p.add_argument('--snapshot',type=Path,required=True)
    p=sub.add_parser('import');p.add_argument('--binary',type=Path,default=DEFAULT_BINARY);p.add_argument('--native-config',type=Path,required=True);p.add_argument('--auth-dir',type=Path,required=True);p.add_argument('--manager-data-dir',type=Path);p.add_argument('--destination',type=Path,required=True);p.add_argument('--listen',default='')
    args=parser.parse_args()
    if args.command=='verify':result=verify(args.snapshot)
    elif args.command=='backup':
        inspection=json.loads(run_binary(args.binary,['--inspect-config','--config',args.config]));result=snapshot(args.config,args.data_dir,args.snapshot,inspection)
    elif args.command=='restore':result=restore(args.snapshot,args.data_dir,args.config,args.binary)
    else:result=import_data(args.binary,args.native_config,args.auth_dir,args.manager_data_dir,args.destination,args.listen)
    print(json.dumps({'ok':True,'result':result},indent=2))
if __name__=='__main__':
    try:main()
    except (OSError,ValueError,sqlite3.Error,subprocess.CalledProcessError,zipfile.BadZipFile) as error:print('ERROR:',error,file=sys.stderr);sys.exit(1)