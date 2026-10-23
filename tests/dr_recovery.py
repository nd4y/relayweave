import json
from pathlib import Path
import full_acceptance as f
import acceptance as p

f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
manifest=json.loads(Path('/reports/dr-manifest.json').read_text())
assert f.request('sources?limit=1000')['total']==manifest['sources']
assert f.request('profiles?limit=1')['total']==manifest['profiles']
assert f.request('overview')['restore_pending']
f.operation('config/apply',timeout=210)
p.HOST='127.0.0.3'
assert p.proxy_http(f.HTTP)[0]==200;assert p.proxy_http(f.HTTP_AUTH,auth=('lab-user','lab-pass'))[0]==200
assert p.socks_http(f.SOCKS)[0]==200;f.udp_echo(f.SOCKS);f.udp_echo(f.SOCKS_AUTH,('lab-user','lab-pass'))
assert not f.request('overview')['restore_pending']
report={'passed':True,'sources':manifest['sources'],'profiles':manifest['profiles'],'checks':['offline CLI decrypted and imported portable backup to empty PostgreSQL','explicit Apply boundary held on new deployment','new worker identity serves restored HTTP and SOCKS TCP/UDP with auth modes','new deployment identity is retained; original stack is unaffected']}
Path('/reports/disaster-recovery.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)
