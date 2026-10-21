"""Every supported protocol/transport on both real versions through the full app."""
import json
import time
from pathlib import Path
import full_acceptance as f
import acceptance as p

def main():
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 ids=json.loads(Path('/reports/integration-ids.json').read_text());policy=f.request('policies/'+ids['policy_primary']);pool=f.request('pools/'+policy['data']['pool_id']);original=pool['data']
 source=f.create('sources','Protocol matrix fixture',{'url':'http://matrix-subscription:9004/subscription.json','allow_private':True,'interval_seconds':0})
 f.operation('sources/'+source['id']+'/refresh')
 profiles=[x for x in f.request('profiles?limit=1000')['items'] if x['data']['source_id']==source['id']]
 assert len(profiles)==13 and all(x['data']['supported'] for x in profiles)
 results=[]
 try:
  for version in ['26.10.17','26.9.30']:
   for item in profiles:
    item=f.request('profiles/'+item['id']);f.patch(item,{'core_version':version})
    pool=f.request('pools/'+pool['id']);f.patch(pool,{'source_ids':[source['id']],'aggregate_id':'','profile_ids':[item['id']]})
    started=time.monotonic();f.operation('config/apply',timeout=210)
    assert p.proxy_http(f.HTTP)[0]==200;assert p.socks_http(f.SOCKS)[0]==200
    if item['data']['udp']:f.udp_echo(f.SOCKS)
    result={'case':item['name'],'version':version,'tcp':True,'udp':bool(item['data']['udp']),'seconds':round(time.monotonic()-started,3)};results.append(result);print(json.dumps(result),flush=True)
 finally:
  pool=f.request('pools/'+pool['id']);f.patch(pool,original);f.operation('config/apply',timeout=210)
 report={'passed':True,'checks':results,'scope':'13 real server configurations x2 client versions, both worker rollouts; HTTP forward/SOCKS TCP and UDP where protocol supports it. Synthetic isolated server credentials only.'}
 Path('/reports/protocol-matrix.json').write_text(json.dumps(report,indent=2))
 print(json.dumps({'passed':True,'cases':len(results)}),flush=True)

if __name__=='__main__':main()
