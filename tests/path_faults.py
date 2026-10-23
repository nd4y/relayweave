"""Real refusal/blackhole and independent target failures; only own TLS shapers change."""
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.request

for line in (Path('/home/av/build/relayweave')/'integration.env').read_text().splitlines():
 key,_,value=line.partition('=')
 if key=='ADMIN_PASSWORD':os.environ[key]=value
os.environ.update(HTTP_PORT='48080',HTTP_AUTH_PORT='48081',SOCKS_PORT='41080',SOCKS_AUTH_PORT='41081')
import full_acceptance as f
import acceptance as p
from advanced_acceptance import until, echo

def docker(*args):return subprocess.check_output(['docker',*args],timeout=60).decode()
def guard(name):
 assert name in ('relayweave-shaper-a-1','relayweave-shaper-b-1')
 obj=json.loads(docker('inspect',name))[0];labels=obj['Config']['Labels']
 assert labels['com.docker.compose.project']=='relayweave' and labels['com.docker.compose.service'] in ('shaper-a','shaper-b')
 return obj
def restore(name):
 obj=guard(name)
 if obj['State']['Paused']:docker('unpause',name)
 if not obj['State']['Running']:docker('start',name)
 rate(name,200 if name.endswith('-a-1') else 20)
def rate(name,mbps):
 port=19096 if name.endswith('-a-1') else 19097
 request=urllib.request.Request('http://127.0.0.1:%d/rate'%port,data=json.dumps({'mbps':mbps}).encode(),headers={'Content-Type':'application/json'},method='POST')
 with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=5) as response:assert response.status==200
def control_responds(name):
 port=19096 if name.endswith('-a-1') else 19097
 try:
  with urllib.request.build_opener(urllib.request.ProxyHandler({})).open('http://127.0.0.1:%d/healthz'%port,timeout=.25) as response:return response.status==200
 except OSError:return False

def main():
 for name in ('relayweave-shaper-a-1','relayweave-shaper-b-1'):restore(name)
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 ids=json.loads(Path('reports/integration-ids.json').read_text())
 policy=f.request('policies/'+ids['policy_primary']);original=dict(policy['data'])
 # GET masks URL paths and query values; restore this fixture's known URLs.
 original['probe_url']='http://origin:9000/healthz'
 original['speed_url']='http://origin:9000/bytes?n=8388608'
 sources=[];pool=None;records=[];failure=None;cleanup_failure=None
 old,_=p.socks_connect(f.SOCKS_AUTH,target_port=9001,auth=('lab-user','lab-pass'))
 try:
  for name in ('a','b'):
   source=f.create('sources','Path fault fixture '+name,{'url':'http://subscription:9004/subscription?upstream=shaper-'+name,'allow_private':True,'interval_seconds':0})
   sources.append(source);f.operation('sources/'+source['id']+'/refresh')
  profiles=[x for x in f.request('profiles?limit=1000')['items'] if x['data']['source_id'] in {s['id'] for s in sources}]
  assert len(profiles)==2
  mapping={x['id']:'relayweave-'+x['data']['host']+'-1' for x in profiles}
  pool=f.create('pools','Path fault pool',{'source_ids':[s['id'] for s in sources]})
  f.patch(policy,{'pool_id':pool['id'],'mode':'fallback','active_paths':1,'warm_reserve':1,'probe_interval_seconds':10,'speed_url':''})
  f.operation('config/apply')
  def status():return p.json_url(f.STATUS_URL)
  def ready():return all(any(c['profile_id']==v['id'] and c['healthy'] for c in status()['cores']) for v in profiles)
  def active():return next(x for x in status()['policies'] if x['id']==policy['id'])['ready_paths'][0]
  def synchronize(profile):
   before=next(c['last_probe_at'] for c in status()['cores'] if c['profile_id']==profile and c['healthy'])
   until(lambda:any(c['profile_id']==profile and c['healthy'] and c['last_probe_at']>before for c in status()['cores']),30)
  def verified_active():
   # Selection can change during the wait for a fresh health result. Verify
   # the actual data-plane choice from its connection counter before faulting.
   for _ in range(8):
    selected=active();synchronize(selected)
    before={c['profile_id']:c['connections'] for c in status()['cores'] if c['profile_id'] in mapping}
    assert p.socks_http(f.SOCKS)[0]==200
    after={c['profile_id']:c['connections'] for c in status()['cores'] if c['profile_id'] in mapping}
    used=[key for key,value in after.items() if value>before.get(key,0)]
    assert len(used)==1,('Could not verify one actual selected profile',used)
    if used[0]==selected and active()==selected:return selected
   raise AssertionError('Active profile did not stabilize after fresh health checks')
  for mode,threshold in [('fast_upstream_close',5),('blackhole_with_attempts',20),('background_blackhole',120)]:
   until(ready,40);profile=verified_active();name=mapping[profile];guard(name)
   injection=time.monotonic();process=None
   if mode=='fast_upstream_close':
    started=time.monotonic();rate(name,0)
   else:
    # Docker metadata fsync may lag the actual pause. Observe the lost control
    # response directly, then measure forwarding recovery independently of CLI.
    process=subprocess.Popen(['docker','pause',name],stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    while control_responds(name):
     assert time.monotonic()-injection<90,'Fault injection did not become observable'
     time.sleep(.05)
    started=time.monotonic()
   injection_seconds=time.monotonic()-injection;attempts=0;recovered=False
   try:
    while time.monotonic()-started<max(40,threshold+10):
     if mode=='background_blackhole':
      paths=next(x for x in status()['policies'] if x['id']==policy['id'])['ready_paths']
      recovered=bool(paths) and profile not in paths
     else:
      attempts+=1
      try:
       forwarded=p.socks_http(f.SOCKS)[0]==200
       confirmed_unhealthy=not any(c['profile_id']==profile and c['healthy'] for c in status()['cores'])
       recovered=forwarded and confirmed_unhealthy
      except (OSError,EOFError,AssertionError):pass
     if recovered:break
     time.sleep(.1 if mode!='background_blackhole' else .25)
    elapsed=time.monotonic()-started
    echo(old,b'independent-core-survives')
    record={'case':mode,'seconds':elapsed,'injection_seconds':injection_seconds,'limit_seconds':threshold,'attempts':attempts,'actual_selected_profile_verified':True,'passed':recovered and elapsed<=threshold}
    records.append(record);print(json.dumps(record),flush=True)
   finally:
    if process:
     output,error=process.communicate(timeout=90);assert process.returncode==0,(output,error)
     assert guard(name)['State']['Paused']
    restore(name)
  until(ready,40)
  current=f.request('policies/'+policy['id']);f.patch(current,{'probe_url':'http://origin:9000/missing-health-target','probe_fallback_urls':['http://origin:9000/healthz']})
  f.operation('config/apply');until(ready,30)
  # A single failed destination must not quarantine either healthy proxy.
  for _ in range(8):
   try:
    stream,_=p.socks_connect(f.SOCKS,target_port=9999)
    with stream:
     stream.settimeout(4);stream.sendall(b'unavailable-destination');assert not stream.recv(1)
   except (OSError,EOFError,AssertionError):pass
   assert p.socks_http(f.SOCKS)[0]==200
  time.sleep(12);assert ready();echo(old,b'target-failure-isolated')
  records.append({'case':'independent_health_target_and_destination_failures','passed':True})
 except BaseException as exc:
  failure=repr(exc);raise
 finally:
  old.close()
  try:
   for name in ('relayweave-shaper-a-1','relayweave-shaper-b-1'):restore(name)
   current=f.request('policies/'+policy['id']);f.patch(current,original);f.operation('config/apply')
   if pool:
    current=f.request('pools/'+pool['id']);f.request('pools/'+pool['id'],'DELETE',version=current['version'])
   for source in sources:
    current=f.request('sources/'+source['id']);f.request('sources/'+source['id'],'DELETE',version=current['version'])
  except Exception as exc:
   cleanup_failure=repr(exc)
   if failure is None:raise
  finally:
   Path('reports/path-faults.json').write_text(json.dumps({'passed':len(records)==4 and all(x['passed'] for x in records) and not failure and not cleanup_failure,'checks':records,'error':failure,'cleanup_error':cleanup_failure,'scope':'Actual immediate TLS upstream close and Dockerpause of own synthetic shapers. Fault starts just after a successful health probe. Pause timing starts at the first observed lost control response, not delayed Docker metadata acknowledgement.'},indent=2))
 assert all(x['passed'] for x in records),records

if __name__=='__main__':main()
