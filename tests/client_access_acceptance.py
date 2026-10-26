"""Synthetic only: scoped access, actual endpoints, auth and token lifecycle."""
import base64,json,os,socket,urllib.request,urllib.error
from pathlib import Path
import full_acceptance as f
import acceptance as protocol
f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
root=f.BASE.split('/api/v1/')[0]
public_client=urllib.request.build_opener(urllib.request.ProxyHandler({}))
checks=[];created=[];tokens=[]
def check(name):checks.append(name);print('PASS '+name,flush=True)
def public(path,expected=200):
 try:
  with public_client.open(root+path,timeout=15) as response:status=response.status;headers=response.headers;body=response.read()
 except urllib.error.HTTPError as error:status=error.code;headers=error.headers;body=error.read()
 assert status==expected,('public status',status,expected)
 return headers,json.loads(body)
try:
 meta=f.request('client-access');assert meta['default_ports']=={'http':f.HTTP,'socks5':f.SOCKS}
 listener=next(row for row in meta['listeners'] if row['protocol']=='http' and row['port']==f.HTTP)
 assert '127.0.0.1' in listener['hosts']
 chosen=f.request('listeners/'+listener['id']);policy=chosen['data']['policy_id']
 grant={'listener':listener['id'],'host':'127.0.0.1','password':'','hours':1,'confirm_secrets':True}
 rendered=f.request('client-access/render','POST',grant)
 uri=urllib.parse.urlsplit(rendered['uri']);assert uri.hostname=='127.0.0.1' and uri.port==f.HTTP
 config=json.loads(rendered['config']);assert len(config['outbounds'])==1 and config['outbounds'][0]['server_port']==f.HTTP
 payload=rendered['throne_uri'].split('/',3)[3];payload+='='*((-len(payload))%4);native=json.loads(base64.urlsafe_b64decode(payload));assert native['type']=='http' and native['server_port']==f.HTTP
 assert all(k in {'type','tag','server','server_port','version','username','password','tls'} for k in native)
 assert protocol.proxy_http(f.HTTP,'/healthz')[0]==200
 check('rendered endpoint/Throne JSON matches actual custom HTTP bind')
 f.request('client-access/render','POST',dict(grant,source_url='https://secret.invalid/private'),expected=400)
 f.request('client-access/render','POST',dict(grant,host='user@host/private'),expected=400)
 f.request('client-access/subscriptions','POST',dict(grant,confirm_secrets=False),expected=400)
 f.request('client-access/subscriptions','POST',dict(grant,hours=2161),expected=400)
 check('unknown fields, unconfigured hosts, secret consent and expiry bounds')
 # Cookies alone cannot mutate exports without the existing CSRF token.
 request=urllib.request.Request(f.BASE+'client-access/render',data=json.dumps(grant).encode(),headers={'Content-Type':'application/json'})
 try:f.client.open(request,timeout=15);raise AssertionError('CSRF bypass')
 except urllib.error.HTTPError as error:assert error.code==403
 check('existing administrator CSRF enforced')
 token=f.request('client-access/subscriptions','POST',grant,expected=201);tokens.append(token)
 headers,download=public(token['path']);assert download==config and headers['Cache-Control']=='no-store'
 raw=json.dumps(f.request('client-access'));assert token['path'] not in raw and 'password' not in raw
 check('autonomous scoped JSON without admin cookie; list does not reveal token/credentials')
 f.request('client-access/'+token['id'],'DELETE');tokens.remove(token);public(token['path'],404)
 check('revoked URL no longer authorizes export')
 password='p:@/% ?я'
 auth=f.create('listeners','Topology export authenticated SOCKS',{'protocol':'socks5','listen':'0.0.0.0:'+str(f.SOCKS_AUTH),'policy_id':policy,'username':'export-user','password':password,'udp':True});created.append(auth)
 private=f.create('listeners','Topology private not exported',{'protocol':'http','listen':'0.0.0.0:8083','policy_id':policy});created.append(private)
 auth_grant=dict(grant,listener=auth['id'],password=password)
 f.request('client-access/render','POST',auth_grant,expected=409)
 f.operation('config/apply',timeout=240)
 f.request('client-access/render','POST',dict(grant,listener=private['id']),expected=400)
 f.request('client-access/render','POST',dict(auth_grant,password='wrong'),expected=400)
 f.request('client-access/render','POST',dict(auth_grant,confirm_secrets=False),expected=400)
 rendered_auth=f.request('client-access/render','POST',auth_grant)
 parsed=urllib.parse.urlsplit(rendered_auth['uri']);assert parsed.port==f.SOCKS_AUTH and urllib.parse.unquote(parsed.username)=='export-user' and urllib.parse.unquote(parsed.password)==password
 stream,_=protocol.socks_connect(f.SOCKS_AUTH,auth=('export-user',password))
 with stream:
  stream.sendall(b'GET /healthz HTTP/1.1\r\nHost: origin\r\nConnection: close\r\n\r\n');assert protocol.response(stream).status==200
 check('draft rejected, private 8083 excluded, encoded auth URI reaches actual SOCKS listener')
 token=f.request('client-access/subscriptions','POST',auth_grant,expected=201);tokens.append(token)
 _,body=public(token['path']);assert body['outbounds'][0]['password']==password
 current=f.request('listeners/'+auth['id']);f.patch(current,{'enabled':False});public(token['path'],410)
 check('listener/auth configuration change invalidates scoped subscription')
 report={'passed':True,'checks':checks,'published_http_port':f.HTTP,'published_socks_port':f.SOCKS,'auth_socks_port':f.SOCKS_AUTH,'provider_data_exported':False}

finally:
 for token in tokens:
  try:f.request('client-access/'+token['id'],'DELETE')
  except Exception:pass
 for item in created:
  current=f.request('listeners/'+item['id']);f.request('listeners/'+item['id'],'DELETE',version=current['version'])
 if created:f.operation('config/apply',timeout=240)

Path('/reports/client-access-acceptance.json').write_text(json.dumps(report,indent=2));print(json.dumps({'passed':True,'checks':len(checks)}),flush=True)
