#!/usr/bin/env python3
"""Smoke-test the packaged release-candidate binary using disposable data and one loopback port."""
import platform
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import time
import urllib.error
import urllib.request
import uuid

ROOT=Path(__file__).resolve().parents[1]

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary',default=str(ROOT/'dist'/('windows-amd64-portable/cliproxyapi-plus.exe' if os.name=='nt' else ('linux-'+('arm64' if platform.machine() in ['aarch64','arm64'] else 'amd64')+'-portable/cliproxyapi-plus'))))
    args=parser.parse_args()
    binary=Path(args.binary).resolve()
    if not binary.is_file(): raise RuntimeError('Build the release-candidate binary first')
    work=ROOT/'.build/smoke'/uuid.uuid4().hex
    work.mkdir(parents=True)
    config=work/'config.yaml';data=work/'data'
    with socket.socket() as reserve:
        reserve.bind(('127.0.0.1',0));port=reserve.getsockname()[1]
    address=f'127.0.0.1:{port}'
    env={name:value for name,value in os.environ.items() if name.upper() in {
        'PATH','SYSTEMROOT','WINDIR','TEMP','TMP','USERPROFILE','LOCALAPPDATA','APPDATA','HOME','PATHEXT'}}
    # Reproduce installation on a machine with no Go SDK or external IANA database.
    empty_tz=work/'no-host-tzdata';empty_tz.mkdir()
    env.update(GOROOT=str(empty_tz),ZONEINFO=str(empty_tz),WRITABLE_PATH=str(work))
    flags=subprocess.CREATE_NO_WINDOW if os.name=='nt' else 0
    subprocess.run([str(binary),'--init','--config',str(config),'--data-dir',str(data),'--listen',address],
                   env=env,check=True,creationflags=flags,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    stdout=(work/'stdout.log').open('wb');stderr=(work/'stderr.log').open('wb')
    process=subprocess.Popen([str(binary),'--config',str(config),'--data-dir',str(data)],cwd=work,env=env,
                             creationflags=flags,stdout=stdout,stderr=stderr)
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
    def request(path,key='',payload=None):
        req=urllib.request.Request(f'http://{address}{path}',data=json.dumps(payload).encode() if payload is not None else None)
        if payload is not None:req.add_header('Content-Type','application/json')
        if key:req.add_header('Authorization','Bearer '+key)
        try:
            with opener.open(req,timeout=5) as response:return response.status,response.read(),dict(response.headers)
        except urllib.error.HTTPError as error:return error.code,error.read(),dict(error.headers)
    try:
        deadline=time.monotonic()+30
        while True:
            if process.poll() is not None:raise RuntimeError('Binary exited before readiness; inspect '+str(work))
            try:
                if request('/plus-api/ready')[0]==200:break
            except OSError:pass
            if time.monotonic()>deadline:raise RuntimeError('Readiness timeout; inspect '+str(work))
            time.sleep(.05)
        admin=(data/'admin.key').read_text(encoding='utf-8').strip()
        api=(data/'api.key').read_text(encoding='utf-8').strip()
        internal=(data/'core-management.key').read_text(encoding='utf-8').strip()
        status,html,_=request('/management.html')
        assert status==200 and len(html)>100000,(status,len(html))
        status,payload,headers=request('/v0/management/config',admin)
        assert status==200,(status,payload[:200])
        assert internal.encode() not in payload,'Internal credential leaked in management response'
        assert request('/v0/management/config',internal)[0]==401
        assert request('/v0/management/config',api)[0]==401
        assert request('/v1/models',api)[0]==200
        assert request('/v1/models',admin)[0]==401
        timezone_checks=[]
        for page,include in [
            ('usage-analytics',{'summary':True,'timeline':True,'hourly_distribution':True,'heatmap':True,'granularity':'hour'}),
            ('monitoring',{'summary':True,'account_stats':True,'filter_options':True,'granularity':'hour'}),
        ]:
            for zone in ['Asia/Shanghai','America/New_York']:
                status,body,_=request('/v0/management/monitoring/analytics',admin,{'from_ms':1767225600000,'to_ms':1767312000000,'time_zone':zone,'include':include})
                assert status==200,(page,zone,status,body[:500])
                assert isinstance(json.loads(body).get('summary'),dict),(page,body[:500])
                timezone_checks.append({'page':page,'time_zone':zone,'status':status})
        status,body,_=request('/v0/management/monitoring/analytics',admin,{'from_ms':1767225600000,'to_ms':1767312000000,'time_zone':'Mars/Olympus','include':{'summary':True}})
        # Preserve the native analytics error contract for genuinely invalid zones.
        assert status==500 and json.loads(body).get('code')=='invalid_time_zone',(status,body[:500])

        status,v8_payload,_=request('/v8/management/config/api-keys',admin)
        # A new empty native config has no provider api-keys subtree; preserve the core 404.
        assert status==404 and json.loads(v8_payload).get('error')=='not_found',(status,v8_payload)
        assert request('/v8/management/config/api-keys')[0]==401
        assert request('/v8/management/config/upstreams',admin)[0]==404
        status,payload,_=request('/plus-api/version')
        identity=json.loads(payload)
        lock=json.loads((ROOT/'upstream.lock.json').read_text(encoding='utf-8'))
        assert status==200 and identity['phase']=='release-candidate'
        assert identity['core_commit']==lock['projects']['cliproxyapi']['commit']
        assert identity['manager_commit']==lock['projects']['cpa-manager-plus']['commit']
        lowered={k.lower():v for k,v in headers.items()}
        assert lowered.get('x-cpa-version')==identity['core_version'],lowered
        status,body,_=request('/v0/management/logging-to-file',admin)
        assert status==200 and json.loads(body).get('logging-to-file') is True,(status,body)
        application_log=work/'logs/main.log'
        deadline=time.monotonic()+5
        markers=[b'/v0/management/usage-queue?count=100',b'[startup] starting background workers']
        while True:
            content=application_log.read_bytes() if application_log.is_file() else b''
            if all(marker in content for marker in markers):break
            if time.monotonic()>deadline:raise AssertionError('Core and Manager logs did not reach '+str(application_log))
            time.sleep(.05)
        console=(work/'stdout.log').read_bytes()+(work/'stderr.log').read_bytes()
        assert all(marker not in console for marker in markers),'Core/Manager runtime logs still flood the console'
        logging_checks={'application_log':str(application_log),'core_and_manager_in_file':True,'runtime_markers_absent_from_console':True,'log_bytes':len(content)}
        listeners=None
        if os.name=='nt':
            powershell=Path(os.environ['SYSTEMROOT'])/'System32/WindowsPowerShell/v1.0/powershell.exe'
            command=f'Get-NetTCPConnection -OwningProcess {process.pid} -State Listen | Select-Object LocalAddress,LocalPort | ConvertTo-Json -Compress'
            result=subprocess.run([str(powershell),'-NoProfile','-Command',command],capture_output=True,text=True,encoding="utf-8",errors="replace",check=True,creationflags=flags)
            listeners=json.loads(result.stdout)
            if isinstance(listeners,dict):listeners=[listeners]
            assert len(listeners)==1 and listeners[0]['LocalPort']==port,listeners
        report={'passed':True,'binary':str(binary),'identity':identity,'frontend_bytes':len(html),
                'logging_checks':logging_checks,'timezone_checks':timezone_checks,'host_timezone_sources_disabled':True,'listeners':listeners,'core_response_headers':headers,'data_dir':str(data),'log_dir':str(work),
                'cleanup':'Disposable process is terminated after this binary-level smoke. Graceful shutdown is covered by test-e2e.'}
        (ROOT/'.build/binary-smoke-report.json').write_text(json.dumps(report,indent=2),encoding='utf-8')
        print(json.dumps(report,indent=2))
    finally:
        if process.poll() is None:
            process.terminate()
            try:process.wait(timeout=10)
            except subprocess.TimeoutExpired:process.kill();process.wait(timeout=5)
        stdout.close();stderr.close()

if __name__=='__main__':main()