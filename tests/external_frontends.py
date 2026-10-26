"""Actual published HTTP CONNECT and SOCKS5 TCP/UDP over authorized providers."""
import json,socket,ssl,time
from pathlib import Path
import full_acceptance as f
import acceptance as p
from external_live import dns,safe_failure

def https(stream):
 with ssl.create_default_context().wrap_socket(stream,server_hostname='www.gstatic.com') as tls:
  tls.settimeout(12);tls.sendall(b'GET /generate_204 HTTP/1.1\r\nHost: www.gstatic.com\r\nConnection: close\r\n\r\n')
  line=b''
  while b'\r\n' not in line and len(line)<4096:line+=tls.recv(512)
  assert line.startswith(b'HTTP/1.1 204'), 'external_https_status'

def http():
 with socket.create_connection(('127.0.0.1',f.HTTP),timeout=12) as stream:
  stream.sendall(b'CONNECT www.gstatic.com:443 HTTP/1.1\r\nHost: www.gstatic.com:443\r\n\r\n')
  reply=b''
  while not reply.endswith(b'\r\n\r\n') and len(reply)<32768:reply+=p.exact(stream,1)
  assert reply.startswith(b'HTTP/1.1 200'),'http_connect_failed';https(stream)

def socks():
 stream,_=p.socks_connect(f.SOCKS,target='www.gstatic.com',target_port=443)
 with stream:https(stream)

def main():
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 report=json.loads(Path('/reports/external-live.json').read_text());assert report['complete']
 listeners=[s for s in f.request('listeners')['items'] if s['name'] in ('External HTTP','External SOCKS')]
 try:
  for record in report['providers']:
   number=record['provider'];policy=next(s for s in f.request('policies')['items'] if s['name']=='Provider policy '+str(number))
   try:
    for listener in listeners:
     current=f.request('listeners/'+listener['id']);f.patch(current,{'enabled':True,'policy_id':policy['id']})
    f.operation('config/apply',timeout=210)
    http();record['http_connect_https']=True;socks();record['socks_https']=True;dns(f.SOCKS);record['udp_dns']=True
   except Exception as error:record['frontend_error']=safe_failure(error)
   print(json.dumps({k:v for k,v in record.items() if k in ('provider','http_connect_https','socks_https','udp_dns','frontend_error')}),flush=True)
 finally:
  for listener in listeners:
   current=f.request('listeners/'+listener['id']);f.patch(current,{'enabled':False})
  f.operation('config/apply',timeout=210)
  report['frontend_passed']=all(r.get('http_connect_https') and r.get('socks_https') and r.get('udp_dns') for r in report['providers'])
  report['passed']=report['passed'] and report['frontend_passed']
  status=p.json_url(f.STATUS_URL)
  report['probe_body_bytes_current_process']=status['probe_bytes']
  report['probe_budget_reserved_bytes']=status.get('probe_budget_reserved_bytes')
  assert status['probe_bytes']<=128<<20
  if report['probe_budget_reserved_bytes'] is not None:assert report['probe_budget_reserved_bytes']<=128<<20
  Path('/reports/external-live.json').write_text(json.dumps(report,indent=2))
 assert report['passed'],'external_frontend_check_failed'

if __name__=='__main__':
 try:main()
 except Exception as error:print(json.dumps({'code':safe_failure(error)}),flush=True);raise SystemExit(1)
