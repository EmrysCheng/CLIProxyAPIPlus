#!/usr/bin/env python3
"""Disposable real-binary backup, import, paired upgrade and rollback rehearsal."""
import argparse
from contextlib import contextmanager,closing
import json,os,shutil,socket,sqlite3,subprocess,time,urllib.request,uuid
from pathlib import Path
import data as maintenance
import upgrade

ROOT=Path(__file__).resolve().parents[1]
def reserve():
 with socket.socket() as s:s.bind(('127.0.0.1',0));return '127.0.0.1:'+str(s.getsockname()[1])

@contextmanager
def running(binary,config,data,address,work,discard_logs=False):
 env={k:v for k,v in os.environ.items() if k.upper() in ['PATH','SYSTEMROOT','WINDIR','TEMP','TMP','USERPROFILE','LOCALAPPDATA','APPDATA','HOME','PATHEXT']}
 flags=subprocess.CREATE_NO_WINDOW if os.name=='nt' else 0
 with (Path(os.devnull) if discard_logs else work/('runtime-'+uuid.uuid4().hex+'.log')).open('wb') as log:
  proc=subprocess.Popen([str(binary),'--config',str(config),'--data-dir',str(data),'--listen',address],cwd=work,env=env,stdout=log,stderr=log,creationflags=flags)
  opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
  try:
   deadline=time.monotonic()+40
   while True:
    if proc.poll() is not None:raise RuntimeError('Test binary exited before readiness: '+str(work))
    try:
     with opener.open('http://'+address+'/plus-api/ready',timeout=3) as response:
      if response.status==200:break
    except OSError:pass
    if time.monotonic()>deadline:raise RuntimeError('Test readiness deadline exceeded')
    time.sleep(.05)
   yield opener
  finally:
   if proc.poll() is None:
    proc.terminate()
    try:proc.wait(timeout=15)
    except subprocess.TimeoutExpired:proc.kill();proc.wait(timeout=5)


def assert_retained(directory,key):
 assert (directory/'data.key').read_bytes()==key,'Encryption key changed'
 assert (directory/'auths/fixture.json').read_text(encoding='utf-8')=='{"fixture":"no-real-credentials"}'
 with closing(sqlite3.connect(directory/'usage.sqlite')) as db:
  assert db.execute('select value from plus_fixture').fetchone()[0]=='retained'
  assert db.execute('PRAGMA quick_check').fetchone()[0]=='ok'

def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--binary',type=Path,default=maintenance.DEFAULT_BINARY);a=p.parse_args();binary=a.binary.resolve()
 work=ROOT/'.build/maintenance-smoke'/uuid.uuid4().hex;work.mkdir(parents=True)
 installation=work/'installation';installation.mkdir();current=installation/binary.name;shutil.copyfile(binary,current);os.chmod(current,0o755)
 config=work/'config.yaml';data=work/'data';address=reserve();checks=[]
 maintenance.run_binary(current,['--init','--config',config,'--data-dir',data,'--listen',address])
 with running(current,config,data,address,work):
  info=json.loads(maintenance.run_binary(current,['--inspect-config','--config',config]))
  try:maintenance.snapshot(config,data,work/'live.zip',info)
  except ValueError as err:assert 'locked' in str(err)
  else:raise AssertionError('Live Manager did not block offline snapshot')
 checks.append('live Go-owned database lock blocks Python backup')
 (data/'auths').mkdir(exist_ok=True);(data/'auths/fixture.json').write_text('{"fixture":"no-real-credentials"}',encoding='utf-8')
 with closing(sqlite3.connect(data/'usage.sqlite')) as db:
  db.execute('create table plus_fixture(value text)');db.execute("insert into plus_fixture values('retained')");db.commit()
 key=(data/'data.key').read_bytes();before=maintenance.sha(config)
 snapshot=work/'snapshot.zip';maintenance.snapshot(config,data,snapshot,info);maintenance.verify(snapshot)
 maintenance.restore(snapshot,work/'restored-data',work/'restored.yaml',current)
 assert_retained(work/'restored-data',key)
 with running(current,work/'restored.yaml',work/'restored-data',reserve(),work):pass
 checks.append('verified SQLite/key/auth/config restore and restart')
 imported=work/'imported';maintenance.import_data(current,config,data/'auths',data,imported,reserve())
 assert maintenance.sha(config)==before,'Import changed original config'
 assert_retained(imported/'data',key)
 imported_address=reserve()
 with running(current,imported/'config.yaml',imported/'data',imported_address,work) as opener:
  admin=(imported/'data/admin.key').read_text().strip()
  req=urllib.request.Request('http://'+imported_address+'/plus-api/releases',headers={'Authorization':'Bearer '+admin})
  with opener.open(req,timeout=5) as response:assert response.status==200
 checks.append('native config and Manager data import preserves encryption and source')
 args=argparse.Namespace(binary=current,candidate=binary,config=config,data_dir=data,sha256=maintenance.sha(binary),state_dir=work/'upgrades',apply=True,simulate_failure=True)
 oldhash=maintenance.sha(current)
 try:upgrade.install(args)
 except RuntimeError as err:assert 'Injected post-migration failure' in str(err)
 else:raise AssertionError('Failure injection did not fail')
 assert maintenance.sha(current)==oldhash;assert_retained(data,key)
 journals=list((work/'upgrades').glob('*/journal.json'));assert len(journals)==1
 assert json.loads(journals[0].read_text())['state_status']=='rolled_back'
 checks.append('post-migration failure atomically restores paired binary and data')
 args.simulate_failure=False;receipt=upgrade.install(args)
 assert receipt['state_status']=='awaiting_restart_and_health_check';assert_retained(data,key)
 with running(current,config,data,address,work):upgrade.check_running('http://'+address,(ROOT/'VERSION').read_text().strip())
 checks.append('successful offline upgrade then explicit restart and health/version check')
 missing=work/'missing-key';shutil.copytree(data,missing);(missing/'data.key').unlink()
 try:maintenance.import_data(current,config,data/'auths',missing,work/'invalid-import',reserve())
 except ValueError as err:assert 'data.key' in str(err)
 else:raise AssertionError('Missing encryption key accepted')
 checks.append('missing original encryption key fails closed')
 report={'passed':True,'checks':checks,'work':str(work),'uses_real_provider':False,'cleanup':'All test processes terminated. Disposable data/backup retained for audit; do not publish secrets.'}
 (ROOT/'.build/maintenance-smoke-report.json').write_text(json.dumps(report,indent=2),encoding='utf-8');print(json.dumps(report,indent=2))
if __name__=='__main__':main()
