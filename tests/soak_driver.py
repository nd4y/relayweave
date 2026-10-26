"""Real 72h controller. Host-side Docker access is confined to labelled test objects.

No clock simulation, Docker socket mount in the product, host network mutation,
global service restarts or unrelated container operations are used.
"""
import concurrent.futures,hashlib,json,os,re,socket,statistics,subprocess,sys,threading,time,urllib.request
from datetime import datetime,timezone
from pathlib import Path

SMOKE=sys.argv[1:]==['--smoke'];assert not sys.argv[1:] or SMOKE
ROOT=Path(__file__).resolve().parents[1]
RUN_ID=os.environ.get('SOAK_RUN_ID','smoke-build19' if SMOKE else '72h-build19');assert re.fullmatch('[a-z0-9-]{1,32}',RUN_ID)
# Active append logs live on persistent Linux storage. Synced Windows files are
# published snapshots, never the authoritative running journal.
OUT=Path.home()/'relayweave-soak-reports'/RUN_ID;OUT.mkdir(parents=True,mode=0o700,exist_ok=True)
MIRROR=ROOT/'reports'/('soak-'+RUN_ID);MIRROR.mkdir(exist_ok=True)
for line in (Path.home()/'relayweave-soak.env').read_text().splitlines():
 if '=' in line:
  key,value=line.split('=',1)
  if key=='ADMIN_PASSWORD':os.environ[key]=value
os.environ.update(API_URL='http://127.0.0.1:54443/api/v1/',HTTP_PORT='54080',HTTP_AUTH_PORT='54081',SOCKS_PORT='53080',SOCKS_AUTH_PORT='53081',WORKER_STATUS_URL='http://127.0.0.1:59095/status')
import full_acceptance as f
import acceptance as p

DURATION=600 if SMOKE else 72*3600
CHAOS_INTERVAL=30 if SMOKE else 600
DOCKER_OPERATION_TIMEOUT=120
NAMES={v:'relayweave-soak-'+v+'-1' for v in ('worker','control-plane','database')}
STOP=threading.Event();lock=threading.Lock();events=[];resources=[]
state={'complete':False,'passed':False,'scope':'10min harness smoke; not the 72h gate' if SMOKE else 'real 72h chaos/soak','required_seconds':DURATION,'started_at':datetime.now(timezone.utc).isoformat(),'traffic_cycles':0,'steady_failures':0,'disruption_failures':0,'max_recovery_seconds':0,'max_total_recovery_seconds':0,'docker_operation_timeout_seconds':DOCKER_OPERATION_TIMEOUT,'chaos_completed':0}
allowed_until=0.;started=time.monotonic();tcp=[];udp=[]

def docker(*args,input=None):
 # Docker acknowledgement can lag process/data-plane recovery. Keep this
 # control-call deadline separate from both forwarding deadlines below.
 return subprocess.check_output(['docker',*args],input=input,timeout=DOCKER_OPERATION_TIMEOUT,stderr=subprocess.DEVNULL).decode()

def guard(service):
 obj=json.loads(docker('inspect',NAMES[service]))[0]
 labels=obj['Config'].get('Labels',{})
 assert labels.get('com.docker.compose.project')=='relayweave-soak' and labels.get('io.relayweave.soak')=='true'
 return obj

def save():
 with lock:
  state['elapsed_seconds']=round(time.monotonic()-started,3);state['updated_at']=datetime.now(timezone.utc).isoformat()
  data=json.dumps(state,indent=2)
 temp=OUT/'soak-status.tmp';temp.write_text(data);temp.replace(OUT/'soak-status.json')
 try:
  temp=MIRROR/'soak-status.tmp';temp.write_text(data);temp.replace(MIRROR/'soak-status.json')
 except OSError:pass

def record(value):
 with lock:
  with (OUT/'soak-events.jsonl').open('a') as stream:stream.write(json.dumps(dict(at=datetime.now(timezone.utc).isoformat(),**value))+'\n')

def new_traffic():
 assert p.proxy_http(f.HTTP)[0]==200
 assert p.socks_http(f.SOCKS)[0]==200
 assert p.socks_http(f.SOCKS_AUTH,auth=('lab-user','lab-pass'))[0]==200
 f.udp_echo(f.SOCKS);f.udp_echo(f.SOCKS_AUTH,('lab-user','lab-pass'))

def refill():
 while len(tcp)<128:
  stream,_=p.socks_connect(f.SOCKS,target='origin',target_port=9001);stream.settimeout(3);tcp.append(stream)
 while len(udp)<64:
  control,bound=p.socks_connect(f.SOCKS,command=3,target='0.0.0.0',target_port=0)
  sock=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);sock.settimeout(2);udp.append((control,sock,bound))

def sustained():
 payload=os.urandom(2048);failures=[]
 def tcp_exchange(stream):
  try:stream.sendall(payload);assert p.exact(stream,len(payload))==payload;return None
  except Exception:return stream
 frame=b'\0\0\0'+p.address('origin',9002)+payload[:128]
 def udp_exchange(item):
  control,sock,bound=item
  try:sock.sendto(frame,bound);assert sock.recv(8192).endswith(payload[:128]);return None
  except Exception:return item
 with concurrent.futures.ThreadPoolExecutor(max_workers=32) as executor:
  for stream in executor.map(tcp_exchange,list(tcp)):
   if stream is not None:tcp.remove(stream);stream.close();failures.append(True)
  for item in executor.map(udp_exchange,list(udp)):
   if item is not None:udp.remove(item);item[0].close();item[1].close();failures.append(True)
 if failures:raise RuntimeError('sustained_connection_interrupted')

def sample_resources():
 obj=guard('worker');status=p.json_url(f.STATUS_URL)
 # A read-only helper shares only this verified test worker's PID namespace.
 source=b'import json,os\np=1\ns=open("/proc/1/status").read().splitlines()\nprint(json.dumps({"fd":len(os.listdir("/proc/1/fd")),"rss_kib":int(next(x for x in s if x.startswith("VmRSS:")).split()[1])}))\n'
 raw=docker('run','--rm','-i','--label','io.relayweave.soak=true','--pid=container:'+NAMES['worker'],'--network=none','--read-only','--user','65532:65532','--cap-drop','ALL','--security-opt','no-new-privileges','--cpus','0.25','--memory','64m','python:3.13-slim-bookworm','python','-',input=source)
 metrics=p.json_url(f.STATUS_URL);row=dict(json.loads(raw),elapsed=time.monotonic()-started,processes=len(status['cores']))
 with urllib.request.urlopen('http://127.0.0.1:59095/metrics',timeout=5) as response:
  match=re.search(rb'^proxy_worker_goroutines (\d+)$',response.read(),re.M)
 if match:row['goroutines']=int(match[1])
 resources.append(row)
 with (OUT/'soak-resources.jsonl').open('a') as stream:stream.write(json.dumps(row)+'\n')

def chaos():
 global allowed_until
 actions=['api_outage','core_crash','database_outage','network_partition','worker_restart']
 index=0
 while not STOP.wait(CHAOS_INTERVAL):
  action=actions[index%len(actions)];index+=1
  disruptive=action not in ('api_outage','database_outage')
  action_started=time.monotonic()
  with lock:allowed_until=action_started+120 if disruptive else 0
  record({'chaos':action,'phase':'start'})
  try:
   if action in ('api_outage','database_outage'):
    service='control-plane' if action=='api_outage' else 'database';guard(service);docker('stop','-t','10',NAMES[service])
    try:STOP.wait(5 if SMOKE else 60)
    finally:docker('start',NAMES[service])
   elif action=='worker_restart':guard('worker');docker('restart','-t','10',NAMES['worker'])
   elif action=='network_partition':
    obj=guard('worker');assert 'proxy-platform-poc_front' in obj['NetworkSettings']['Networks']
    docker('network','disconnect','proxy-platform-poc_front',NAMES['worker'])
    try:STOP.wait(5 if SMOKE else 15)
    finally:docker('network','connect','proxy-platform-poc_front',NAMES['worker'])
   else:
    guard('worker');pid=next(c['pid'] for c in p.json_url(f.STATUS_URL)['cores'] if c['ready'] and c['version']=='26.10.17');assert isinstance(pid,int) and pid>1
    source=('import os,signal\npid='+str(pid)+'\nc=open("/proc/%d/cmdline"%pid,"rb").read()\nassert b"/opt/xray/builtin/xray" in c and b"/data/runtime/" in c\nos.kill(pid,signal.SIGKILL)\n').encode()
    docker('run','--rm','-i','--label','io.relayweave.soak=true','--pid=container:'+NAMES['worker'],'--network=none','--read-only','--user','65532:65532','--cap-drop','ALL','--security-opt','no-new-privileges','--cpus','0.25','--memory','64m','python:3.13-slim-bookworm','python','-',input=source)
   recovery=time.monotonic()
   while True:
    try:new_traffic();break
    except Exception:
     if time.monotonic()-recovery>45:raise RuntimeError('self_healing_deadline')
     if STOP.wait(1):return
   elapsed=time.monotonic()-recovery
   total_elapsed=time.monotonic()-action_started
   if disruptive and total_elapsed>120:raise RuntimeError('end_to_end_self_healing_deadline')
   with lock:
    state['max_recovery_seconds']=max(state['max_recovery_seconds'],elapsed)
    if disruptive:state['max_total_recovery_seconds']=max(state['max_total_recovery_seconds'],total_elapsed)
    state['chaos_completed']+=1;allowed_until=time.monotonic()+8 if disruptive else 0
   record({'chaos':action,'phase':'recovered','seconds':elapsed,'total_seconds':total_elapsed,'forwarding_disruption_expected':disruptive})
  except Exception as error:
   record({'chaos':action,'phase':'failed','error':type(error).__name__});state['chaos_error']=action;STOP.set();return

def main():
 state['runtime_images']={service:guard(service)['Image'] for service in NAMES}
 state['controller_sha256']=hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
 assert not (OUT/'soak-status.json').exists(),'Preserve prior run and use a new report directory before restarting the real-duration gate'
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 new_traffic();refill();sustained();save()
 record({'phase':'started','required_seconds':DURATION,'tcp':128,'udp':64})
 thread=threading.Thread(target=chaos,daemon=True);thread.start();last_resource=-999
 try:
  while time.monotonic()-started<DURATION and not STOP.is_set():
   try:
    new_traffic();sustained();refill()
   except Exception as error:
    with lock:
     expected=time.monotonic()<allowed_until
     key='disruption_failures' if expected else 'steady_failures';state[key]+=1
    record({'traffic_failure':type(error).__name__,'expected_disruption':expected})
   state['traffic_cycles']+=1
   if time.monotonic()-last_resource>=(30 if SMOKE else 300):
    try:sample_resources();last_resource=time.monotonic()
    except Exception as error:record({'resource_sample_error':type(error).__name__})
   save();STOP.wait(2)
  settled=[r for r in resources if r['elapsed']>=6*3600]
  growth_ok=False
  if len(settled)>=100:
   n=max(10,len(settled)//5);early=settled[:n];late=settled[-n:]
   growth_ok=statistics.median(r['rss_kib'] for r in late)<=statistics.median(r['rss_kib'] for r in early)*1.25+32768 and statistics.median(r['fd'] for r in late)<=statistics.median(r['fd'] for r in early)+64 and max(r['processes'] for r in settled)<=32 and all('goroutines' in r for r in early+late) and statistics.median(r.get('goroutines',0) for r in late)<=statistics.median(r.get('goroutines',0) for r in early)+64
  state['complete']=time.monotonic()-started>=DURATION;state['resource_growth_passed']=None if SMOKE else growth_ok
  if SMOKE:growth_ok=len(resources)>=5 and max(r['rss_kib'] for r in resources)<2*1024*1024 and max(r['processes'] for r in resources)<=32
  if SMOKE:state['smoke_resource_bounds_passed']=growth_ok
 finally:
  STOP.set();thread.join(timeout=100)
  for stream in tcp:stream.close()
  for control,sock,_ in udp:control.close();sock.close()
  save()
 state['passed']=state['complete'] and state['steady_failures']==0 and state['chaos_completed']>=(5 if SMOKE else 100) and growth_ok and 'chaos_error' not in state
 save()
 for name in ('soak-status.json','soak-events.jsonl','soak-resources.jsonl'):
  source=OUT/name
  if source.exists():
   temp=MIRROR/(name+'.tmp');temp.write_bytes(source.read_bytes());temp.replace(MIRROR/name)
 print(json.dumps(state),flush=True)
 if not state['passed']:raise SystemExit(1)

if __name__=='__main__':main()
