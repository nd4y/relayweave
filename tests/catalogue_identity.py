"""Preserved manual intent and transactional cache invalidation in actual PostgreSQL."""
import json
from pathlib import Path
import full_acceptance as f

def main():
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 source=f.create('sources','Identity contract fixture',{'url':'http://subscription:9004/subscription','allow_private':True,'interval_seconds':0,'core_version':'26.10.17'})
 f.operation('sources/'+source['id']+'/refresh')
 def profiles():return [p for p in f.request('profiles?limit=1000')['items'] if p['data']['source_id']==source['id'] and p['data']['present']]
 profile=profiles()[0];identity=profile['id']
 pool=f.create('pools','Identity cache fixture',{'source_ids':[source['id']]})
 def preview():return f.request('pools/'+pool['id']+'/preview')
 assert preview()['total']==1
 f.patch(profile,{'enabled':False,'core_version':'26.9.30'})
 assert preview()['total']==0,'Profile disable did not invalidate the decoded catalogue'
 source=f.request('sources/'+source['id']);f.patch(source,{'url':'http://subscription:9004/subscription?network=ws'})
 changed=f.operation('sources/'+source['id']+'/refresh');assert changed['requires_review']
 f.operation('sources/'+source['id']+'/approve')
 current=profiles();assert len(current)==1
 profile=current[0]
 assert profile['id']==identity and not profile['data']['enabled'] and profile['data']['core_override'] and profile['data']['core_version']=='26.9.30'
 assert profile['data']['transport']=='ws'
 assert preview()['total']==0
 f.patch(profile,{'enabled':True});assert preview()['total']==1
 profile=f.request('profiles/'+identity);f.patch(profile,{'enabled':False})
 source=f.request('sources/'+source['id']);f.patch(source,{'url':'http://subscription:9004/subscription?network=ws&variant=ambiguous'})
 f.operation('sources/'+source['id']+'/refresh')
 source=f.request('sources/'+source['id'])
 if source['data'].get('pending_snapshot'):f.operation('sources/'+source['id']+'/approve')
 current=profiles();assert len(current)==2 and all(not p['data']['enabled'] for p in current)
 assert preview()['total']==0
 f.request('pools/'+pool['id'],'DELETE',version=f.request('pools/'+pool['id'])['version'])
 f.request('sources/'+source['id'],'DELETE',version=f.request('sources/'+source['id'])['version'])
 report={'passed':True,'checks':['disable and enable invalidate decoded catalogue immediately','transport update requires review and preserves profile ID plus manual core override','ambiguous endpoint replacement holds disabled manual intent','source and pool cleanup']}
 Path('/reports/catalogue-identity.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)

if __name__=='__main__':main()
