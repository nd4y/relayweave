"""Allowlisted synthetic fixture diagnosis; excludes configuration and secrets."""
import json
import urllib.request
with urllib.request.urlopen('http://127.0.0.1:39090/status',timeout=10) as response:data=json.load(response)
keys=['id','profile_id','version','ready','healthy','state','active','failures','starts','restarts','last_probe_at','circuit_until']
print(json.dumps({'active_connections':data.get('active_connections'),'drained':data.get('drained'),'lease_until':data.get('lease_until'),'policies':[{k:v for k,v in row.items() if k in ['id','mode','ready_paths','catalog_size']} for row in data.get('policies',[])],'cores':[{k:v for k,v in row.items() if k in keys} for row in data.get('cores',[])]},indent=2))
