import json
import time
from pathlib import Path
import full_acceptance as f
import acceptance as p

def main():
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 ids=json.loads(Path('/reports/integration-ids.json').read_text());policy=f.request('policies/'+ids['policy_primary']);pool=f.request('pools/'+policy['data']['pool_id']);original=pool['data']
 source=f.create('sources','Canary discovery fixture',{'url':'http://matrix-subscription:9004/canary.json','allow_private':True,'interval_seconds':0})
 f.operation('sources/'+source['id']+'/refresh')
 profiles=[x for x in f.request('profiles?limit=1000')['items'] if x['data']['source_id']==source['id']];assert len(profiles)==17
 peak=0;start=time.monotonic()
 try:
  f.patch(pool,{'source_ids':[source['id']],'aggregate_id':'','profile_ids':[]})
  op=f.request('config/apply','POST',{},expected=202)
  while time.monotonic()-start<210:
   for worker in f.request('workers')['items']:
    if worker['online']:peak=max(peak,len(worker['status'].get('cores',[])))
   current=f.request('operations/'+op['id'])['data']
   if current['status'] in ('failed','succeeded'):assert current['status']=='succeeded',current;break
   time.sleep(.5)
  else:raise TimeoutError('canary discovery')
  assert p.socks_http(f.SOCKS)[0]==200;f.udp_echo(f.SOCKS);assert peak<=32
  report={'passed':True,'bad_candidates_before_healthy':16,'elapsed_seconds':time.monotonic()-start,'peak_processes_per_worker':peak,'scope':'Actual closed upstream port on first16 candidates, healthy17th; both enrolled workers.'}
  Path('/reports/canary-discovery.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)
 finally:
  pool=f.request('pools/'+pool['id']);f.patch(pool,original);f.operation('config/apply',timeout=210)

if __name__=='__main__':main()
