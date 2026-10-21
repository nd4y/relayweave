"""Enforce real published listener ACLs using the observed Docker client address."""
import ipaddress
import json
import os
import socket
from pathlib import Path
import subprocess

for line in Path('/home/av/build/relayweave/integration.env').read_text().splitlines():
 key,_,value=line.partition('=')
 if key=='ADMIN_PASSWORD':os.environ[key]=value
os.environ.update(HTTP_PORT='48080',HTTP_AUTH_PORT='48081',SOCKS_PORT='41080',SOCKS_AUTH_PORT='41081')
import full_acceptance as f
import acceptance as p
from advanced_acceptance import echo

def main():
 obj=json.loads(subprocess.check_output(['docker','inspect','relayweave-worker-1']))[0]
 assert obj['Config']['Labels'].get('com.docker.compose.project')=='relayweave' and obj['Config']['Labels'].get('io.relayweave.test-stack')=='true'
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 listeners=[x for x in f.request('listeners')['items'] if x['data']['listen'].endswith((':1080',':8080'))]
 assert len(listeners)==2
 original={x['id']:list(x['data'].get('allow_cidrs') or []) for x in listeners}
 stream,_=p.socks_connect(f.SOCKS,target_port=9001);echo(stream,b'prior-to-acl')
 code='''import ipaddress,json,socket
ips=set()
for family,table in [(socket.AF_INET,"tcp"),(socket.AF_INET6,"tcp6")]:
 for line in open("/proc/self/net/"+table).read().splitlines()[1:]:
  fields=line.split()
  if fields[3]!="01" or int(fields[1].split(":")[1],16)!=1080:continue
  raw=bytes.fromhex(fields[2].split(":")[0])
  packed=b"".join(raw[i:i+4][::-1] for i in range(0,len(raw),4))
  address=ipaddress.ip_address(socket.inet_ntop(family,packed))
  if isinstance(address,ipaddress.IPv6Address) and address.ipv4_mapped:address=address.ipv4_mapped
  ips.add(str(address))
print(json.dumps(sorted(ips)))
'''
 addresses=json.loads(subprocess.check_output(['docker','run','--rm','--network=container:relayweave-worker-1','--read-only','--cap-drop=ALL','--security-opt=no-new-privileges','--cpus=.25','--memory=64m','python:3.13-slim-bookworm','python','-c',code],timeout=90))
 assert len(addresses)==1,addresses
 peer=ipaddress.ip_address(addresses[0]);cidr=str(ipaddress.ip_network(f'{peer}/{peer.max_prefixlen}'))
 try:
  for listener in listeners:f.patch(listener,{'allow_cidrs':[cidr]})
  f.operation('config/apply');assert p.proxy_http(f.HTTP)[0]==200 and p.socks_http(f.SOCKS)[0]==200;f.udp_echo(f.SOCKS)
  for listener in listeners:
   current=f.request('listeners/'+listener['id']);f.patch(current,{'allow_cidrs':['::1/128']})
  f.operation('config/apply');assert p.proxy_http(f.HTTP)[0]==403
  with socket.create_connection(('127.0.0.1',f.SOCKS),timeout=5) as denied:
   try:denied.sendall(b'\x05\x01\x00');reply=denied.recv(2)
   except (ConnectionResetError,BrokenPipeError):reply=b''
   assert reply==b'','Nonmatching ACL admitted a SOCKS handshake'
  echo(stream,b'existing-acl-session')
 finally:
  stream.close()
  for listener in listeners:
   current=f.request('listeners/'+listener['id']);f.patch(current,{'allow_cidrs':original[listener['id']]})
  f.operation('config/apply')
 assert p.proxy_http(f.HTTP)[0]==200 and p.socks_http(f.SOCKS)[0]==200;f.udp_echo(f.SOCKS)
 report={'passed':True,'observed_client_family':'IPv'+str(peer.version),'checks':['exact observed Docker peer address allowed for HTTP/SOCKS/UDP','nonmatching real loopback IPv6 ACL rejects new HTTP/SOCKS','existing TCP drains across ACL change','original listener ACL restored and traffic verified']}
 Path('reports/listener-acl.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)

if __name__=='__main__':main()
