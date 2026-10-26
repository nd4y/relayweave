import json
import os
from pathlib import Path

root=Path('/matrix')
base=next(o for o in json.loads((root/'public/subscription.json').read_text())['outbounds'] if o['tag']=='vless-tls')
paths=[]
for name in ['a','b']:
 o=json.loads(json.dumps(base));o['tag']='path-'+name;o['settings']['vnext'][0].update(address='shaper-'+name,port=2443);paths.append(o)
for strategy in ['a','b','rr','leastload']:
 config={'log':{'loglevel':'warning','access':'none'},'inbounds':[{'tag':'ingress','listen':'0.0.0.0','port':1080,'protocol':'socks','settings':{'auth':'noauth','udp':True,'ip':'127.0.0.1'}}]}
 if strategy in ['a','b']:config['outbounds']=[paths[0 if strategy=='a' else 1]]
 else:
  config['outbounds']=[{'protocol':'blackhole','tag':'blocked'}]+paths
  config['routing']={'domainStrategy':'AsIs','rules':[{'type':'field','inboundTag':['ingress'],'balancerTag':'balancer'}],'balancers':[{'tag':'balancer','selector':['path-'],'fallbackTag':'path-a','strategy':{'type':'roundRobin' if strategy=='rr' else 'leastLoad','settings':{'expected':1,'maxRTT':'500ms','tolerance':0.1}}}]}
  config['observatory']={'subjectSelector':['path-'],'probeURL':'http://origin:9000/healthz','probeInterval':'10s','enableConcurrency':True}
 target=root/('adaptive-'+strategy+'.json');temporary=target.with_suffix('.new');temporary.write_text(json.dumps(config));os.chmod(temporary,0o600);os.chown(temporary,65532,65532);temporary.replace(target)
print('Prepared fixed A/B, round-robin and Xray leastLoad baselines using identical synthetic VLESS/TLS paths.')
