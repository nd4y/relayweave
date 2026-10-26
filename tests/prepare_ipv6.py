import json,os,subprocess,uuid
from pathlib import Path
root=Path(os.environ['IPV6_DATA_ROOT']);root.mkdir(mode=0o700,exist_ok=True);(root/'public').mkdir(exist_ok=True)
# These generated fixture files are served by a capability-free root process
# and mounted by a separate test client. The parent directory stays private;
# only this synthetic public bind mount needs cross-UID read/traverse access.
(root/'public').chmod(0o755)
obj=json.loads(subprocess.check_output(['docker','inspect','relayweave-ipv6-origin6-1']))[0]
assert obj['Config']['Labels']['com.docker.compose.project']=='relayweave-ipv6'
address=obj['NetworkSettings']['Networks']['relayweave-ipv6_back6']['GlobalIPv6Address'];assert ':' in address
user=str(uuid.uuid4())
config={'log':{'loglevel':'warning'},'inbounds':[{'listen':'0.0.0.0','port':26001,'protocol':'vless','settings':{'clients':[{'id':user}],'decryption':'none'},'streamSettings':{'network':'raw'}}],'outbounds':[{'tag':'block','protocol':'blackhole'},{'tag':'allowed','protocol':'freedom'}],'routing':{'rules':[{'type':'field','ip':[address+'/128'],'port':'9000-9003','outboundTag':'allowed'}]}}
config['log']['access']='none'
config['outbounds'][1]['settings']={'finalRules':[{'action':'allow','network':'tcp','ip':[address],'port':'9000,9001,9003'},{'action':'allow','network':'udp','ip':[address],'port':'9002,9003'}]}
(root/'server.json').write_text(json.dumps(config));os.chmod(root/'server.json',0o644)
subscription={'outbounds':[{'tag':'DE IPv6 target fixture','protocol':'vless','settings':{'vnext':[{'address':'upstream6','port':26001,'users':[{'id':user,'encryption':'none'}]}]},'streamSettings':{'network':'raw','security':'none'}}]}
(root/'public/subscription.json').write_text(json.dumps(subscription));(root/'public/target.json').write_text(json.dumps({'host':address}))
for name in ('subscription.json','target.json'):(root/'public'/name).chmod(0o644)
print('Prepared IPv6 fixture with Docker-allocated ULA and exact origin/port ACL.',flush=True)
