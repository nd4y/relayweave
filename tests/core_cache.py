"""Exercise real worker cache protection, targeting, eviction and re-download."""
import json,time
from pathlib import Path
import full_acceptance as f
import acceptance as p
from advanced_acceptance import until

def denied(worker,version):
 op=f.request('cores/remove','POST',{'worker_id':worker,'version':version},expected=202)
 until(lambda:f.request('operations/'+op['id'])['data']['status'] in ('failed','succeeded'),30)
 result=f.request('operations/'+op['id'])['data'];assert result['status']=='failed' and result['error']=='artifact_cache_in_use',result

def main():
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 workers=[w['id'] for w in f.request('workers')['items'] if w['online']];assert len(workers)==2
 for worker in workers:
  denied(worker,'26.10.17');denied(worker,'26.9.30')
 # Previous-good and retiring instances are deliberately protected until newer
 # successful checkpoints exist and the real drain window has elapsed.
 f.operation('config/apply');f.operation('config/apply');time.sleep(35)
 for worker in workers:
  before=f.operation('cores/cache',{'worker_id':worker})
  item=next(x for x in before['items'] if x['selector']=='26.9.30-compat');assert not item['protected'],item
  after=f.operation('cores/remove',{'worker_id':worker,'version':item['selector']})
  assert all(x['selector']!=item['selector'] for x in after['items'])
  assert after['bytes']<before['bytes']
 assert p.proxy_http(f.HTTP)[0]==200;p.socks_http(f.SOCKS_AUTH,auth=('lab-user','lab-pass'));f.udp_echo(f.SOCKS)
 f.operation('cores/install',{'version':'26.9.30-compat'})
 cache=f.operation('cores/cache',{'worker_id':'worker-1'});assert any(x['selector']=='26.9.30-compat' for x in cache['items'])
 report={'passed':True,'workers':workers,'checks':['builtin removal blocked','active/recovery secondary removal blocked','unused variant removed independently from each worker','active HTTP/SOCKS TCP/UDP unaffected','approval retained and variant downloaded and verified again']}
 Path('/reports/core-cache.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)

if __name__=='__main__':main()
