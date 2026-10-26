import json
import time
from pathlib import Path
import full_acceptance as f

f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
pool=f.request('pools')['items'][0]
start=time.perf_counter();preview=f.request('pools/'+pool['id']+'/preview?limit=1000');elapsed=(time.perf_counter()-start)*1000
assert preview['total']==10000 and len(preview['items'])==1000
report={'passed':elapsed<=300,'cold_pool_preview_ms':elapsed,'total':10000,'returned':1000,'scope':'First pool preview after cold control-plane restart. Readiness includes catalogue read-model bootstrap; startup decode time is recorded separately in structured logs. Same 10k fixture.'}
Path('/reports/scale-cold.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True);assert report['passed']
