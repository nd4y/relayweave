"""Actual upstream auth rotation, stable profile identity and existing TCP drain."""
import base64
import json
from pathlib import Path
import socket
import urllib.request
import full_acceptance as f
import acceptance as p
from advanced_acceptance import until, echo

def control(action):
 req=urllib.request.Request('http://127.0.0.1:19094/'+action,data=b'{}',method='POST')
 with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=10) as response:assert response.status==200

def upstream_status(password):
 with socket.create_connection(('127.0.0.1',19093),timeout=8) as stream:
  token=base64.b64encode(('lab-user:'+password).encode()).decode()
  stream.sendall(('CONNECT origin:9001 HTTP/1.1\r\nHost: origin:9001\r\nProxy-Authorization: Basic '+token+'\r\n\r\n').encode())
  data=b''
  while not data.endswith(b'\r\n\r\n'):data+=p.exact(stream,1)
  return int(data.split(b' ',2)[1])

def main():
 control('reset');f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 ids=json.loads(Path('/reports/integration-ids.json').read_text())
 policy=f.request('policies/'+ids['policy_primary']);original=dict(policy['data']);original['speed_url']='http://origin:9000/bytes?n=8388608'
 source=f.create('sources','Actual credential rotation fixture',{'url':'http://credential-upstream:9008/subscription','allow_private':True,'interval_seconds':0})
 pool=None;stream=None;failure=None;cleanup_failure=None;report={'passed':False}
 try:
  f.operation('sources/'+source['id']+'/refresh')
  profile=next(x for x in f.request('profiles?limit=1000')['items'] if x['data']['source_id']==source['id'])
  pool=f.create('pools','Credential rotation pool',{'source_ids':[source['id']]})
  f.patch(policy,{'pool_id':pool['id'],'speed_url':''});f.operation('config/apply')
  stream,_=p.socks_connect(f.SOCKS,target_port=9001);echo(stream,b'before-rotation')
  assert upstream_status('fixture-old-password')==200
  control('rotate');assert upstream_status('fixture-old-password')==407 and upstream_status('fixture-new-password')==200
  f.operation('sources/'+source['id']+'/refresh')
  current=f.request('profiles/'+profile['id'])
  assert current['data']['present'] and current['data']['enabled'] and current['data']['fingerprint']!=profile['data']['fingerprint']
  def changed():
   assert p.socks_http(f.SOCKS)[0]==200
   return p.proxy_http(f.HTTP)[0]==200
  until(changed,30);echo(stream,b'authenticated-stream-drains')
  assert len([x for x in f.request('profiles?limit=1000')['items'] if x['data']['source_id']==source['id'] and x['data']['present']])==1
  report={'passed':True,'checks':['old upstream credentials actually rejected','new upstream credentials actually accepted','same profile ID with changed credential-sensitive fingerprint','automatic reconcile admits new HTTP/SOCKS TCP connections','existing authenticated TCP stream survives credential rotation']}
 except BaseException as exc:failure=repr(exc);raise
 finally:
  if stream:stream.close()
  try:
   current=f.request('policies/'+policy['id']);f.patch(current,original);f.operation('config/apply')
   if pool:
    current=f.request('pools/'+pool['id']);f.request('pools/'+pool['id'],'DELETE',version=current['version'])
   current=f.request('sources/'+source['id']);f.request('sources/'+source['id'],'DELETE',version=current['version']);control('reset')
  except Exception as exc:
   cleanup_failure=repr(exc)
   if failure is None:raise
  finally:
   report.update(error=failure,cleanup_error=cleanup_failure)
   report['passed']=report['passed'] and failure is None and cleanup_failure is None
   Path('/reports/credential-rotation.json').write_text(json.dumps(report,indent=2))
 print(json.dumps(report),flush=True)

if __name__=='__main__':main()
