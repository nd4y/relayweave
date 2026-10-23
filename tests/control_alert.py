"""Read a real firing control alert during the separately orchestrated outage."""
import json
import urllib.request
from pathlib import Path
with urllib.request.urlopen('http://prometheus:9090/api/v1/alerts',timeout=10) as response:
 data=json.load(response)
assert data['status']=='success'
alerts=[{'name':v['labels']['alertname'],'state':v['state']} for v in data['data']['alerts']]
assert any(v['name']=='ProxyControlUnavailable' and v['state']=='firing' for v in alerts),alerts
with urllib.request.urlopen('http://prometheus:9090/api/v1/query?query=up%7Bjob%3D%22workers%22%7D',timeout=10) as response:
 workers=json.load(response)['data']['result']
expected=set(json.loads(Path('/reports/monitoring.json').read_text())['workers'])
assert {v['metric']['worker'] for v in workers}==expected and all(float(v['value'][1])==1 for v in workers),workers
report={'passed':True,'alerts':alerts,'retained_worker_targets':sorted(expected),'scope':'Actual rule evaluation during the real API/database outage; HTTP SD retains both worker targets.'}
Path('/reports/control-alert.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)
