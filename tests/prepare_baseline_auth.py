"""Only modifies a new file in the isolated synthetic fixture volume."""
import json
from pathlib import Path
p=Path('/lab/baseline-secondary.json');c=json.loads(p.read_text())
for inbound in c['inbounds']:
    if inbound['protocol']=='socks':inbound['settings'].update(auth='password',accounts=[{'user':'lab-user','pass':'lab-pass'}])
    if inbound['protocol']=='http':inbound['settings']['accounts']=[{'user':'lab-user','pass':'lab-pass'}]
out=Path('/lab/baseline-secondary-auth.json');out.write_text(json.dumps(c));out.chmod(0o644)
print('Prepared synthetic authenticated secondary baseline')
