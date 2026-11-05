#!/usr/bin/env python3
"""Diagnostic loopback comparison: original core versus integrated binary."""
import argparse,json,os,statistics,subprocess,time,threading,urllib.request,shutil,uuid
from contextlib import contextmanager
from pathlib import Path
from http.server import ThreadingHTTPServer
import data as maintenance
from smoke_maintenance import running,reserve
from soak import Provider
ROOT=Path(__file__).resolve().parents[1]
@contextmanager
def core_running(binary,config,work,address,key):
 flags=subprocess.CREATE_NO_WINDOW if os.name=='nt' else 0
 env={k:v for k,v in os.environ.items() if k.upper() in ['PATH','SYSTEMROOT','WINDIR','TEMP','TMP','USERPROFILE','LOCALAPPDATA','APPDATA','HOME','PATHEXT']}
 with (work/'core.log').open('wb') as log:
  process=subprocess.Popen([str(binary.resolve()),'--config',str(config),'--local-model'],cwd=work,stdout=log,stderr=log,env=env,creationflags=flags)
  try:
   until=time.monotonic()+30
   while True:
    if process.poll() is not None:raise RuntimeError('Standalone core exited')
    try:
     req=urllib.request.Request('http://'+address+'/v1/models',headers={'Authorization':'Bearer '+key})
     with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=2) as r:
      if r.status==200:break
    except OSError:pass
    if time.monotonic()>until:raise RuntimeError('Standalone core readiness timeout')
    time.sleep(.05)
   yield
  finally:
   if process.poll() is None:process.terminate();process.wait(timeout=15)

def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--binary',type=Path,default=maintenance.DEFAULT_BINARY);p.add_argument('--core-binary',type=Path,required=True);p.add_argument('--samples',type=int,default=200);a=p.parse_args()
 if a.samples<10 or a.samples>100000:raise ValueError('Use 10..100000 samples')
 work=ROOT/'.build/benchmark'/uuid.uuid4().hex;work.mkdir(parents=True);provider=ThreadingHTTPServer(('127.0.0.1',0),Provider);thread=threading.Thread(target=provider.serve_forever,daemon=True);thread.start()
 results={}
 try:
  for mode in ['core','integrated']:
   folder=work/mode;folder.mkdir();config=folder/'config.yaml';data=folder/'data';address=reserve()
   maintenance.run_binary(a.binary,['--init','--config',config,'--data-dir',data,'--listen',address])
   with config.open('a',encoding='utf-8') as f:f.write('\nopenai-compatibility:\n  - name: benchmark\n    base-url: http://127.0.0.1:'+str(provider.server_port)+'/v1\n    api-key-entries:\n      - api-key: fixture\n    models:\n      - name: mock\n        alias: benchmark-model\n')
   key=(data/'api.key').read_text().strip();samples={'json_total_ms':[],'sse_first_event_ms':[],'sse_total_ms':[]}
   ctx=running(a.binary.resolve(),config,data,address,folder,discard_logs=True) if mode=='integrated' else core_running(a.core_binary,config,folder,address,key)
   with ctx:
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for index in range(a.samples+10):
     stream=index%2==0;payload=json.dumps({'model':'benchmark-model','stream':stream,'messages':[{'role':'user','content':'hello'}]}).encode()
     req=urllib.request.Request('http://'+address+'/v1/chat/completions',data=payload,headers={'Authorization':'Bearer '+key,'Content-Type':'application/json'})
     start=time.perf_counter()
     with opener.open(req,timeout=10) as response:
      if stream:response.readline();first=(time.perf_counter()-start)*1000
      body=response.read();assert b'hello' in body or stream
     elapsed=(time.perf_counter()-start)*1000
     if index>=10:
      samples['sse_total_ms' if stream else 'json_total_ms'].append(elapsed)
      if stream:samples['sse_first_event_ms'].append(first)
   results[mode]={k:{'p50':statistics.median(v),'p95':sorted(v)[min(len(v)-1,int(.95*len(v)))]} for k,v in samples.items()}
 finally:provider.shutdown();provider.server_close();thread.join(timeout=5)
 report={'completed':True,'results':results,'samples_per_mode':a.samples,'scope':'sequential loopback diagnostic, mock provider; not a production SLO, memory comparison, concurrency benchmark or 24h acceptance','work':str(work)}
 (ROOT/'.build/benchmark-report.json').write_text(json.dumps(report,indent=2),encoding='utf-8');print(json.dumps(report,indent=2))
if __name__=='__main__':main()
