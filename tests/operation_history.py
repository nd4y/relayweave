"""Real paginated operation history must expose new and active worker jobs."""
import json
from datetime import datetime
from pathlib import Path
import full_acceptance as f
import acceptance as p

def main():
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 assert f.request('operations?limit=1')['total']>=2,'Run after the normal configuration/cache history exists'
 op=f.request('cores/cache','POST',{'worker_id':'worker-1'},expected=202)
 first=f.request('operations?limit=1')['items']
 assert len(first)==1 and first[0]['id']==op['id'],{'error':'New worker operation hidden by old history','expected_id':op['id'],'returned_count':len(first),'first_id':first[0]['id'] if first else None}
 initial_status=first[0]['data']['status']
 f.wait_operation(op,timeout=60)
 first=f.request('operations?limit=1')['items'][0]
 assert first['id']==op['id'] and first['data']['status']=='succeeded','Recent completed operation missing from first page'
 second=f.request('operations?limit=1&offset=1')['items'][0]
 assert second['id']!=first['id'] and datetime.fromisoformat(second['updated_at'].replace('Z','+00:00'))<=datetime.fromisoformat(first['updated_at'].replace('Z','+00:00')),'Pagination skips the recent-history ordering'
 assert p.proxy_http(f.HTTP)[0]==200
 f.udp_echo(f.SOCKS)
 report={'passed':True,'checks':['new cache job visible on first limited page','completed current job remains first','offset returns distinct older history','HTTP/SOCKS UDP unaffected'],'initial_job_status':initial_status,'scope':'Real API-created worker command after normal apply history; page limit1 exposes the same pagination defect as a long history at the default page size.'}
 Path('/reports/operation-history.json').write_text(json.dumps(report,indent=2))
 print(json.dumps(report),flush=True)

if __name__=='__main__':main()
