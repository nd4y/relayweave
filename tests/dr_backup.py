import json
import os
from pathlib import Path
import full_acceptance as f
from advanced_acceptance import raw

f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
password='fixture-disaster-recovery-password'
data=raw('backup',json.dumps({'password':password}).encode())
target=Path(os.environ.get('DR_BACKUP_PATH','/dr/backup.rwb'));target.write_bytes(data);os.chmod(target,0o600)
Path('/reports/dr-manifest.json').write_text(json.dumps({'sources':f.request('sources?limit=1000')['total'],'profiles':f.request('profiles?limit=1')['total'],'bytes':len(data)}))
print('Wrote encrypted synthetic DR backup; no private key or credential printed.')
