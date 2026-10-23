"""Generate synthetic protocol fixtures, with exact isolated destination ACLs."""
import json
import os
import socket
from pathlib import Path

root=Path('/matrix');os.chown(root,0,0);os.chown(root/'keys.txt',0,0);public=root/'public';public.mkdir(exist_ok=True)
keys=dict(line.split(':',1) for line in (root/'keys.txt').read_text().splitlines() if ':' in line)
keys={k.strip().lower():v.strip() for k,v in keys.items()}
private=keys.get('privatekey') or keys.get('private key');pub=keys.get('password (publickey)') or keys.get('password') or keys.get('publickey') or keys.get('public key')
assert private and pub,'Xray key generator output not recognized'
uuid='00000000-0000-4000-8000-000000000001';password='synthetic-matrix-password'
ca=Path('/lab/ca.pem').read_text().strip().splitlines()
tls_server={'certificates':[{'certificateFile':'/lab/ca.pem','keyFile':'/lab/key.pem'}]}
tls_client={'serverName':'lab-upstream','disableSystemRoot':True,'certificates':[{'certificate':ca,'usage':'verify'}]}
inbounds=[];outbounds=[]
cases=[('vless-raw','vless','tcp','none',''),('vless-tls','vless','tcp','tls',''),('vless-ws','vless','ws','tls',''),('vless-grpc','vless','grpc','tls',''),('vless-httpupgrade','vless','httpupgrade','tls',''),('vless-xhttp','vless','xhttp','tls',''),('vless-reality','vless','tcp','reality',''),('vless-vision','vless','tcp','reality','xtls-rprx-vision'),('vmess-tls','vmess','tcp','tls',''),('trojan-tls','trojan','tcp','tls',''),('shadowsocks','shadowsocks','tcp','none',''),('http-auth','http','tcp','none',''),('socks-auth','socks','tcp','none','')]
for n,(name,protocol,network,security,flow) in enumerate(cases):
 port=25000+n
 server={'listen':'0.0.0.0','port':port,'protocol':protocol,'tag':name}
 client={'tag':name,'protocol':protocol}
 if protocol in ('vless','vmess'):
  user={'id':uuid};server_user=dict(user)
  if protocol=='vless':user['encryption']='none'
  else:user['security']='auto'
  if flow:user['flow']=flow;server_user['flow']=flow
  server['settings']={'clients':[server_user]}
  if protocol=='vless':server['settings']['decryption']='none'
  client['settings']={'vnext':[{'address':'matrix-server','port':port,'users':[user]}]}
 elif protocol=='trojan':
  server['settings']={'clients':[{'password':password}]};client['settings']={'servers':[{'address':'matrix-server','port':port,'password':password}]}
 elif protocol=='shadowsocks':
  server['settings']={'method':'aes-128-gcm','password':password,'network':'tcp,udp'};client['settings']={'servers':[{'address':'matrix-server','port':port,'method':'aes-128-gcm','password':password}]}
 else:
  server['settings']={'accounts':[{'user':'matrix','pass':password}]}
  if protocol=='socks':server['settings'].update(auth='password',udp=True,ip='0.0.0.0')
  client['settings']={'servers':[{'address':'matrix-server','port':port,'users':[{'user':'matrix','pass':password}]}]}
 ss={'network':network,'security':security};cs=dict(ss)
 if security=='tls':ss['tlsSettings']=tls_server;cs['tlsSettings']=tls_client
 if security=='reality':
  ss['realitySettings']={'target':'origin:9443','serverNames':['origin'],'privateKey':private,'shortIds':['0123456789abcdef']}
  cs['realitySettings']={'serverName':'origin','fingerprint':'chrome','publicKey':pub,'shortId':'0123456789abcdef'}
 if network=='grpc':ss['grpcSettings']={'serviceName':'fixture'};cs['grpcSettings']={'serviceName':'fixture'}
 if network in ('ws','httpupgrade','xhttp'):
  key={'ws':'wsSettings','httpupgrade':'httpupgradeSettings','xhttp':'xhttpSettings'}[network];ss[key]={'path':'/fixture'};cs[key]={'path':'/fixture'}
 server['streamSettings']=ss;client['streamSettings']=cs;inbounds.append(server);outbounds.append(client)
addresses=sorted({row[4][0] for row in socket.getaddrinfo('origin',None)})
server={'log':{'loglevel':'warning','access':'none'},'inbounds':inbounds,'outbounds':[{'protocol':'freedom','settings':{'finalRules':[{'action':'allow','network':'tcp','ip':addresses,'port':'9000,9001,9443'},{'action':'allow','network':'udp','ip':addresses,'port':'9002,9003,9004,9005,9444'}]}}]}
temporary=root/'server.json.new';temporary.write_text(json.dumps(server));os.chmod(temporary,0o600);os.chown(temporary,65532,65532);temporary.replace(root/'server.json')
(public/'subscription.json').write_text(json.dumps({'outbounds':outbounds}))
good=next(o for o in outbounds if o['tag']=='vless-tls')
canaries=[]
for n in range(16):
 broken=json.loads(json.dumps(good));broken['tag']=f'00 bad canary {n:02d}';broken['settings']['vnext'][0]['port']=1
 broken['streamSettings']['sockopt']={'tcpKeepAliveInterval':n+1};canaries.append(broken)
good=json.loads(json.dumps(good));good['tag']='99 healthy seventeenth';canaries.append(good)
(public/'canary.json').write_text(json.dumps({'outbounds':canaries}))
print('Prepared 13 synthetic protocol/transport cases with isolated origin ACLs; key material not printed.')
