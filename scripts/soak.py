#!/usr/bin/env python3
"""Bounded local mock-provider mixed load. Use --seconds 86400 for a 24h gate."""
from collections import deque
import argparse,concurrent.futures,json,statistics,threading,time,urllib.request,uuid
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
import data as maintenance
from smoke_maintenance import running,reserve
ROOT=Path(__file__).resolve().parents[1]
class Provider(BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_POST(self):
  body=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))))
  if body.get('stream'):
   data=b'data: {"id":"soak","object":"chat.completion.chunk","model":"mock","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}\n\ndata: {"id":"soak","object":"chat.completion.chunk","model":"mock","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}\n\ndata: [DONE]\n\n'
   self.send_response(200);self.send_header('Content-Type','text/event-stream')
  else:
   data=b'{"id":"soak","object":"chat.completion","model":"mock","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}'
   self.send_response(200);self.send_header('Content-Type','application/json')
  self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)

def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--binary',type=Path,default=maintenance.DEFAULT_BINARY);p.add_argument('--seconds',type=int,default=60);p.add_argument('--workers',type=int,default=4);p.add_argument('--interval',type=float,default=.05);a=p.parse_args()
 if a.seconds<1 or a.workers<1 or a.workers>64 or a.interval<0:raise ValueError('Invalid bounded load parameters')
 work=ROOT/'.build/soak'/uuid.uuid4().hex;work.mkdir(parents=True);config=work/'config.yaml';data=work/'data';address=reserve()
 provider=ThreadingHTTPServer(('127.0.0.1',0),Provider);thread=threading.Thread(target=provider.serve_forever,daemon=True);thread.start()
 maintenance.run_binary(a.binary,['--init','--config',config,'--data-dir',data,'--listen',address])
 with config.open('a',encoding='utf-8') as f:f.write('\nopenai-compatibility:\n  - name: soak\n    base-url: http://127.0.0.1:'+str(provider.server_port)+'/v1\n    api-key-entries:\n      - api-key: no-real-key\n    models:\n      - name: mock\n        alias: soak-model\n')
 latencies=deque(maxlen=100000);failures=[];samples=[];count=0;lock=threading.Lock()
 try:
  with running(a.binary.resolve(),config,data,address,work,discard_logs=True):
   api=(data/'api.key').read_text().strip();admin=(data/'admin.key').read_text().strip();base='http://'+address;end=time.monotonic()+a.seconds
   def req(path,key,body=None):
    headers={'Authorization':'Bearer '+key}
    if body is not None:headers['Content-Type']='application/json'
    r=urllib.request.Request(base+path,data=json.dumps(body).encode() if body is not None else None,headers=headers)
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(r,timeout=20) as response:return response.read()
   def worker(index):
    nonlocal count
    i=index
    while time.monotonic()<end:
     started=time.monotonic()
     try:
      if i%10==0:req('/v0/management/usage',admin)
      elif i%10==1:req('/v1/models',api)
      else:
       payload=req('/v1/chat/completions',api,{'model':'soak-model','stream':i%2==0,'messages':[{'role':'user','content':'hello'}]})
       if b'hello' not in payload:raise ValueError('Missing model output')
      with lock:count+=1;latencies.append((time.monotonic()-started)*1000)
     except Exception as err:
      with lock:
       if len(failures)<100:failures.append(str(err))
     i+=1
     if a.interval:time.sleep(a.interval)
   started=time.monotonic()
   with concurrent.futures.ThreadPoolExecutor(max_workers=a.workers) as pool:
    jobs=[pool.submit(worker,i) for i in range(a.workers)]
    while time.monotonic()<end:
     samples.append(json.loads(req('/plus-api/runtime',admin)));time.sleep(min(5,max(0,end-time.monotonic())))
    for job in jobs:job.result()
   elapsed=time.monotonic()-started
   # Collector is asynchronous. Verify persistence after the final batch window.
   time.sleep(2);usage=json.loads(req('/v0/management/usage',admin));assert usage['total_requests']>0
 finally:provider.shutdown();provider.server_close();thread.join(timeout=5)
 ordered=sorted(latencies)
 report={'passed':not failures,'duration_seconds':elapsed,'requested_seconds':a.seconds,'workers':a.workers,'successful_requests':count,'failures':failures,'requests_per_second':count/elapsed,'latency_sample_scope':'most recent 100000 requests (bounded memory)', 'latency_ms_p50':statistics.median(ordered),'latency_ms_p95':ordered[min(len(ordered)-1,int(len(ordered)*.95))],'runtime_samples':samples,'persisted_model_requests':usage['total_requests'],'long_soak_gate_passed':a.seconds>=86400 and not failures,'work':str(work),'scope':'local mock OpenAI JSON/SSE, model list and usage reads; not real OAuth or a standalone-core benchmark'}
 (ROOT/'.build/soak-report.json').write_text(json.dumps(report,indent=2),encoding='utf-8');print(json.dumps({k:v for k,v in report.items() if k!='runtime_samples'},indent=2))
 if failures:raise RuntimeError('Mixed load reported request failures')
if __name__=='__main__':main()
