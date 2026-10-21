import json,socket,struct,urllib.request
from pathlib import Path
import full_acceptance as f
import acceptance as p

def main():
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 host=json.loads(Path('/fixture/target.json').read_text())['host']
 ids=json.loads(Path('/reports/integration-ids.json').read_text());policy=f.request('policies/'+ids['policy_primary']);original=dict(policy['data'])
 # GET masks query values; restore the known synthetic fixture URL explicitly.
 original['probe_url']='http://origin:9000/healthz'
 original['speed_url']='http://origin:9000/bytes?n=8388608'
 source=f.create('sources','IPv6 target fixture',{'url':'http://ipv6-subscription:9006/subscription.json','allow_private':True,'interval_seconds':0})
 pool=None;records=[];failure=None;cleanup_failure=None
 try:
  f.operation('sources/'+source['id']+'/refresh');pool=f.create('pools','IPv6 target pool',{'source_ids':[source['id']]})
  policy=f.request('policies/'+policy['id']);f.patch(policy,{'pool_id':pool['id'],'probe_url':f'http://[{host}]:9000/healthz','probe_fallback_urls':[],'speed_url':''})
  for version in ('26.10.17','26.9.30'):
   print('IPv6 apply',version,flush=True)
   source=f.request('sources/'+source['id']);f.patch(source,{'core_version':version});f.operation('config/apply',timeout=210)
   print('IPv6 traffic',version,flush=True)
   with socket.create_connection(('127.0.0.1',f.HTTP),timeout=8) as stream:
    stream.sendall(f'GET http://[{host}]:9000/healthz HTTP/1.1\r\nHost: [{host}]\r\nConnection: close\r\n\r\n'.encode());assert p.response(stream).status==200
   stream,_=p.socks_connect(f.SOCKS,target=host,target_port=9001)
   with stream:stream.sendall(b'ipv6-tcp');assert p.exact(stream,8)==b'ipv6-tcp'
   with socket.create_connection(('127.0.0.1',f.HTTP),timeout=8) as stream:
    stream.sendall(f'CONNECT [{host}]:9001 HTTP/1.1\r\nHost: [{host}]:9001\r\n\r\n'.encode());head=b''
    while not head.endswith(b'\r\n\r\n'):head+=p.exact(stream,1)
    assert head.startswith(b'HTTP/1.1 200');stream.sendall(b'ipv6-connect');assert p.exact(stream,12)==b'ipv6-connect'
   control,bound=p.socks_connect(f.SOCKS,command=3,target='0.0.0.0',target_port=0)
   query=b'\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x04test\x00\x00\x1c\x00\x01'
   with control,socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as udp:
    udp.settimeout(5);udp.sendto(b'\0\0\0'+p.address(host,9002)+b'ipv6-udp',bound);assert p.datagram_payload(udp.recv(8192))==b'ipv6-udp'
    udp.sendto(b'\0\0\0'+p.address(host,9003)+query,bound);reply=p.datagram_payload(udp.recv(8192));assert reply[:2]==query[:2] and reply[-16:]==socket.inet_pton(socket.AF_INET6,'::1')
   stream,_=p.socks_connect(f.SOCKS,target=host,target_port=9003)
   with stream:
    stream.sendall(len(query).to_bytes(2,'big')+query);size=int.from_bytes(p.exact(stream,2),'big');reply=p.exact(stream,size);assert reply[:2]==query[:2] and reply[-16:]==socket.inet_pton(socket.AF_INET6,'::1')
   records.append({'version':version,'passed':True,'checks':['HTTP IPv6 literal','HTTP CONNECT IPv6 target','SOCKS5 TCP ATYP6','SOCKS5 UDP ATYP6','AAAA DNS over UDP','AAAA DNS over TCP']});print(json.dumps(records[-1]),flush=True)
 except BaseException as exc:
  failure=repr(exc);raise
 finally:
  try:
   current=f.request('policies/'+policy['id']);f.patch(current,original);f.operation('config/apply',timeout=210)
   if pool:
    current=f.request('pools/'+pool['id']);f.request('pools/'+pool['id'],'DELETE',version=current['version'])
   current=f.request('sources/'+source['id']);f.request('sources/'+source['id'],'DELETE',version=current['version'])
  except Exception as exc:
   cleanup_failure=repr(exc)
   if failure is None:raise
  finally:
   if failure or cleanup_failure:
    Path('/reports/ipv6-failed.json').write_text(json.dumps({'passed':False,'checks':records,'error':failure,'cleanup_error':cleanup_failure},indent=2))
 Path('/reports/ipv6.json').write_text(json.dumps({'passed':len(records)==2,'checks':records,'scope':'Actual IPv6 targets behind isolated Docker upstream; published client-facing ports use IPv4. No host routing/sysctl/default bridge change.'},indent=2))

if __name__=='__main__':main()
