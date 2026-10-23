"""Real failed subscription refreshes retain the catalogue; real TTL expires new flows."""
import json
import time
from datetime import datetime, timezone
from pathlib import Path
import full_acceptance as f
import acceptance as p
from advanced_acceptance import until, tcp_works, echo

def failed_refresh(source_id):
 op=f.request('sources/'+source_id+'/refresh','POST',{},expected=202)
 until(lambda:f.request('operations/'+op['id'])['data']['status'] in ('failed','succeeded'),40)
 assert f.request('operations/'+op['id'])['data']['status']=='failed'

def main():
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 ids=json.loads(Path('/reports/integration-ids.json').read_text())
 policy=f.request('policies/'+ids['policy_primary']);original=dict(policy['data'])
 source=f.create('sources','Failure and TTL fixture',{'url':'http://subscription:9004/subscription','allow_private':True,'interval_seconds':0,'max_stale_seconds':300})
 pool=None;stream=None;records=[]
 try:
  f.operation('sources/'+source['id']+'/refresh')
  profile=next(x for x in f.request('profiles?limit=1000')['items'] if x['data']['source_id']==source['id'])
  pool=f.create('pools','Failure and TTL pool',{'source_ids':[source['id']]})
  f.patch(policy,{'pool_id':pool['id']});f.operation('config/apply')
  for endpoint in ('empty','html','unauthorized','timeout'):
   current=f.request('sources/'+source['id']);last_success=current['data']['last_success']
   f.patch(current,{'url':'http://subscription:9004/'+endpoint})
   failed_refresh(source['id'])
   current=f.request('sources/'+source['id'])
   assert current['data']['last_success']==last_success and current['data'].get('last_error')
   assert f.request('profiles/'+profile['id'])['data']['present']
   assert p.proxy_http(f.HTTP)[0]==200 and tcp_works(f.SOCKS)
   f.udp_echo(f.SOCKS)
   records.append({'refresh':endpoint,'catalogue_retained':True,'traffic':True})
   print(json.dumps(records[-1]),flush=True)
  current=f.request('sources/'+source['id']);f.patch(current,{'url':'http://subscription:9004/subscription','max_stale_seconds':60})
  f.operation('sources/'+source['id']+'/refresh');f.operation('config/apply')
  stream,_=p.socks_connect(f.SOCKS,target_port=9001)
  current=f.request('sources/'+source['id']);last_success=datetime.fromisoformat(current['data']['last_success'].replace('Z','+00:00'));f.patch(current,{'url':'http://subscription:9004/empty'})
  failed_refresh(source['id']);start=time.monotonic()
  until(lambda:not tcp_works(f.SOCKS),70)
  assert (datetime.now(timezone.utc)-last_success).total_seconds()>=60,'Expiry happened before the real 60-second TTL'
  echo(stream,b'ttl-existing-stream')
  assert tcp_works(f.SOCKS_AUTH,('lab-user','lab-pass'))
  records.append({'real_ttl_expired':True,'seconds_from_failed_refresh':time.monotonic()-start,'old_tcp_drains':True,'other_source_unaffected':True})
  current=f.request('sources/'+source['id']);f.patch(current,{'url':'http://subscription:9004/subscription'})
  f.operation('sources/'+source['id']+'/refresh');renewed=time.monotonic()
  until(lambda:tcp_works(f.SOCKS),30)
  recovery_seconds=time.monotonic()-renewed
  assert recovery_seconds<=30,'Fresh source did not recover an expired process within 30 seconds'
  assert p.proxy_http(f.HTTP)[0]==200;f.udp_echo(f.SOCKS)
  echo(stream,b'ttl-renewed-existing-stream')
  records.append({'expired_source_refresh_recovers':True,'seconds_after_refresh':recovery_seconds,'tcp_udp':True,'old_tcp_drains':True})
 finally:
  if stream:stream.close()
  current=f.request('policies/'+policy['id']);f.patch(current,original);f.operation('config/apply')
  if pool:
   current=f.request('pools/'+pool['id']);f.request('pools/'+pool['id'],'DELETE',version=current['version'])
  current=f.request('sources/'+source['id']);f.request('sources/'+source['id'],'DELETE',version=current['version'])
 report={'passed':True,'checks':records,'scope':'Synthetic source only; real HTTP failures and elapsed TTL, no clock simulation.'}
 Path('/reports/source-failures.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)

if __name__=='__main__':main()
