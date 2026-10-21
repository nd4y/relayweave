"""Approved artifact selectors, actual download/integrity/version validation and drain."""
import json
from pathlib import Path
import full_acceptance as f
import acceptance as p
from advanced_acceptance import until

def main():
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 core=next(a for a in f.request('cores')['items'] if a['version']=='26.9.30' and not a.get('variant'))
 artifact=dict(core,variant='compat',repository='XTLS/Xray-core',tag='v26.9.30',license='MPL-2.0',capabilities=['tcp','udp','vless'])
 denied=dict(artifact,repository='unapproved/fork')
 f.request('cores/approve','POST',denied,expected=400)
 broken=dict(artifact,variant='broken',sha256='0'*64)
 op=f.request('cores/approve','POST',broken,expected=202)
 until(lambda:f.request('operations/'+op['id'])['data']['status'] in ('failed','succeeded'),100)
 assert f.request('operations/'+op['id'])['data']['status']=='failed'
 assert not any(a.get('variant')=='broken' for a in f.request('cores')['items'])
 f.operation('cores/approve',artifact,timeout=200)
 items=f.request('cores')['items'];assert any(a.get('variant')=='compat' and a['binary_sha256']==core['binary_sha256'] for a in items)
 ids=json.loads(Path('/reports/integration-ids.json').read_text())
 secondary=next(x for x in f.request('profiles?limit=1000')['items'] if x['data']['source_id']==ids['source_secondary'])
 old,_=p.socks_connect(f.SOCKS_AUTH,target_port=9001,auth=('lab-user','lab-pass'))
 try:
  f.patch(secondary,{'core_version':'26.9.30-compat'})
  def ready():
   workers=[w for w in f.request('workers')['items'] if w['online']]
   return len(workers)==2 and all(any(c['core']=='xray_26_9_30_compat' and c['ready'] for c in w['status']['cores']) for w in workers)
  until(ready,150)
  f.operation('profiles/'+secondary['id']+'/test')
  assert p.socks_http(f.SOCKS_AUTH,auth=('lab-user','lab-pass'))[0]==200;f.udp_echo(f.SOCKS_AUTH,('lab-user','lab-pass'))
  old.sendall(b'variant-drain');assert p.exact(old,13)==b'variant-drain'
  secondary=f.request('profiles/'+secondary['id']);f.patch(secondary,{'core_version':''})
  until(lambda:any(c['core']=='xray_26_9_30' and c['ready'] for c in p.json_url(f.STATUS_URL)['cores']),30)
  old.sendall(b'rollback');assert p.exact(old,8)==b'rollback'
 finally:old.close()
 report={'passed':True,'checks':['unapproved GitHub repository rejected','bad archive checksum rejected before execution and approval','variant downloaded and version checked on worker','both workers applied a per-profile artifact selector','TCP and UDP through variant; old TCP drains across selection and rollback'], 'scope':'Registration fixture uses the exact official secondary release bytes under a variant selector; it does not claim validation of an arbitrary third-party build.'}
 Path('/reports/core-variants.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)

if __name__=='__main__':main()
