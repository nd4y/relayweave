"""Controller for an explicitly labelled 64-MiB tmpfs test volume, never host disk."""
import json
import os
from pathlib import Path
import subprocess
import time

root=Path('/home/av/build/relayweave')
for line in (root/'integration.env').read_text().splitlines():
 key,_,value=line.partition('=')
 if key=='ADMIN_PASSWORD':os.environ[key]=value
os.environ.update(HTTP_PORT='48080',HTTP_AUTH_PORT='48081',SOCKS_PORT='41080',SOCKS_AUTH_PORT='41081')
import full_acceptance as f
import acceptance as p
from advanced_acceptance import until, echo

volume='relayweave-worker2_fault-data'
def docker(*args):return subprocess.check_output(['docker',*args],timeout=120).decode()
def helper(code):
 return subprocess.check_output(['docker','run','--rm','-i','--network=none','--read-only','--cap-drop=ALL','--security-opt=no-new-privileges','--user=65532:65532','--cpus=0.25','--memory=128m','-v',volume+':/fault','python:3.13-slim-bookworm','python','-'],input=code.encode(),timeout=120).decode()

def traffic():
 previous=p.HOST;p.HOST='127.0.0.2'
 try:
  assert p.proxy_http(61081,auth=('lab-user','lab-pass'))[0]==200
  c,_=p.socks_connect(62080,target_port=9001)
  with c:echo(c,b'disk-pressure')
  f.udp_echo(62081,('lab-user','lab-pass'))
 finally:p.HOST=previous

def main():
 obj=json.loads(docker('volume','inspect',volume))[0]
 assert obj['Labels'].get('io.relayweave.fault-fixture')=='true'
 assert obj['Labels'].get('com.docker.compose.project')=='relayweave-worker2'
 assert obj['Options']['type']=='tmpfs' and obj['Options']['o'].startswith('size=64m,')
 worker=json.loads(docker('inspect','relayweave-worker2-worker-1'))[0]
 assert any(v.get('Name')==volume and v['Destination']=='/data' for v in worker['Mounts'])
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 until(lambda:any(w['id']=='worker-2' and w['online'] and w['generation']>0 for w in f.request('workers')['items']),90)
 traffic();old_host=p.HOST;p.HOST='127.0.0.2'
 try:old,_=p.socks_connect(62080,target_port=9001)
 finally:p.HOST=old_host
 try:
  filled=json.loads(helper('import errno,json,os\np="/fault/enospc-fixture"\nsize=0\nwith open(p,"wb",buffering=0) as s:\n try:\n  while True:size+=s.write(bytes(1<<20))\n except OSError as e:\n  assert e.errno==errno.ENOSPC\nprint(json.dumps({"filled_bytes":size,"enospc":True}))\n'))
  assert filled['enospc'] and filled['filled_bytes']<=64<<20
  op=f.request('config/apply','POST',{},expected=202)
  until(lambda:f.request('operations/'+op['id'])['data']['status'] in ('failed','succeeded'),60)
  result=f.request('operations/'+op['id'])['data']
  assert result['status']=='failed' and result['error']=='worker_validation_failed',result
  for _ in range(10):
   traffic();echo(old,b'existing-during-enospc');time.sleep(.5)
  assert p.proxy_http(f.HTTP)[0]==200
 finally:
  helper('from pathlib import Path\nPath("/fault/enospc-fixture").unlink(missing_ok=True)\n')
  old.close()
 f.operation('config/apply');traffic()
 report={'passed':True,'filled_bytes':filled['filled_bytes'],'limit_bytes':64<<20,'checks':['ENOSPC on bounded worker-state tmpfs only','rollout rejected with diagnostic worker status','existing and new TCP/HTTP/UDP retain allowed configuration','other worker unaffected','apply recovers after test pressure removed']}
 Path('reports/disk-pressure.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)

if __name__=='__main__':main()
