"""Paired adaptive/fixed-best comparison and changed/failed path scenarios."""
import json
import statistics
import time
import urllib.request
from pathlib import Path
import full_acceptance as f
import acceptance as p
from benchmark import measure
from advanced_acceptance import until

def rate(name,mbps):
 req=urllib.request.Request(f'http://127.0.0.1:{19096 if name=="a" else 19097}/rate',data=json.dumps({'mbps':mbps}).encode(),headers={'Content-Type':'application/json'},method='POST')
 with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req,timeout=5) as response:assert response.status==200

def main():
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 ids=json.loads(Path('/reports/integration-ids.json').read_text());policy=f.request('policies/'+ids['policy_primary']);pool=f.request('pools/'+policy['data']['pool_id']);original_policy=policy['data'];original_pool=pool['data']
 sources=[]
 for name in ['a','b']:
  source=f.create('sources','Adaptive channel '+name,{'url':f'http://subscription:9004/subscription?upstream=shaper-{name}','allow_private':True,'interval_seconds':0});f.operation('sources/'+source['id']+'/refresh');sources.append(source)
 profiles=[x for x in f.request('profiles?limit=1000')['items'] if x['data']['source_id'] in [s['id'] for s in sources]];assert len(profiles)==2
 f.patch(pool,{'source_ids':[s['id'] for s in sources],'aggregate_id':'','profile_ids':[]})
 f.patch(policy,{'mode':'throughput','active_paths':2,'warm_reserve':0,'probe_budget_bytes':512<<20,'speed_url':'http://origin:9000/bytes?n=8388608','probe_interval_seconds':10})
 results=[]
 try:
  rate('a',200);rate('b',20);f.operation('config/apply',timeout=210)
  until(lambda:len([c for c in p.json_url(f.STATUS_URL)['cores'] if c['ready'] and c['profile_id'] in [x['id'] for x in profiles]])==2,30)
  for profile in profiles:f.operation('profiles/'+profile['id']+'/speed-test')
  for phase,fast,slow in [('initial','a','b'),('capacity_swapped','b','a')]:
   if phase!='initial':
    rate('a',20);rate('b',200)
    # Let normal health/passive measurements and bounded confirmation react.
    changed=time.monotonic()
    while time.monotonic()-changed<45:
     measure('socks5',f.SOCKS,4<<20,4)
     time.sleep(1)
   pairs=[]
   for trial in range(5):
    pair={};order=['baseline','adaptive'] if trial%2==0 else ['adaptive','baseline']
    for variant in order:
     port=(31801 if fast=='a' else 31802) if variant=='baseline' else f.SOCKS
     if variant=='adaptive':pair['before']=p.json_url(f.STATUS_URL)
     pair[variant]=measure('socks5',port,16<<20,4)
     if variant=='adaptive':pair['after']=p.json_url(f.STATUS_URL)
    pair['ratio']=pair['adaptive']['mbps']/pair['baseline']['mbps'];pairs.append(pair);print(json.dumps({'phase':phase,'trial':trial,'ratio':pair['ratio'],'adaptive_mbps':pair['adaptive']['mbps'],'baseline_mbps':pair['baseline']['mbps']}),flush=True)
   baseline=statistics.median(x['baseline']['mbps'] for x in pairs);adaptive=statistics.median(x['adaptive']['mbps'] for x in pairs)
   comparisons={name:measure('socks5',port,4<<20,4)['mbps'] for name,port in [('round_robin',31803),('xray_leastload',31804)]}
   results.append({'phase':phase,'baseline_mbps':baseline,'adaptive_mbps':adaptive,'ratio':adaptive/baseline,'passed':adaptive/baseline>=.9,'comparisons':comparisons,'pairs':pairs})
  rate('b',0)
  fail_started=time.monotonic()
  until(lambda:p.socks_http(f.SOCKS)[0]==200,15)
  recovery=time.monotonic()-fail_started
  report={'passed':all(x['passed'] for x in results),'groups':results,'new_connection_failover_seconds':recovery,'scope':'Two shared per-direction 200/20Mbps VLESS/TLS paths, capacities swapped without manual reprobe. Five alternating pairs x4 equal16MiB flows versus same-version standalone fixed best path. Equal-size finite-flow workload; one TCP stream is never striped. Separate round-robin and Xray leastLoad references.'}
  Path('/reports/adaptive.json').write_text(json.dumps(report,indent=2));print(json.dumps({'passed':report['passed'],'ratios':[x['ratio'] for x in results],'failover_seconds':recovery}),flush=True);assert report['passed']
 finally:
  rate('a',200);rate('b',20)
  pool=f.request('pools/'+pool['id']);f.patch(pool,original_pool)
  policy=f.request('policies/'+policy['id']);f.patch(policy,original_policy);f.operation('config/apply',timeout=210)

if __name__=='__main__':main()
