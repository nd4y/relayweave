"""Actual two-worker mTLS enrollment, independent traffic and targeted operations."""
import json
import socket
import time
from pathlib import Path
import full_acceptance as f
import acceptance as p
from advanced_acceptance import until

def main():
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 until(lambda:len([w for w in f.request('workers')['items'] if w['online']])==2,60)
 reports=[]
 def traffic(host,port):
  previous=p.HOST;p.HOST=host
  try:
   c,_=p.socks_connect(port,target_port=9001)
   with c:c.sendall(b'multiworker');assert p.exact(c,11)==b'multiworker'
  finally:p.HOST=previous
 for host,port in [('127.0.0.1',f.SOCKS),('127.0.0.2',62080)]:traffic(host,port)
 old,_=p.socks_connect(f.SOCKS,target_port=9001)
 second_status=lambda:p.json_url('http://127.0.0.2:39095/status')
 before=second_status();assert next(w for w in f.request('workers')['items'] if w['id']=='worker-2')['generation']>0
 f.operation('workers/worker-2/drain',{'enabled':True})
 assert second_status()['drained']
 assert not p.json_url(f.STATUS_URL)['drained']
 traffic('127.0.0.1',f.SOCKS)
 p.HOST='127.0.0.2'
 try:
  try:c,_=p.socks_connect(62080,target_port=9001);c.close();raise AssertionError('drained worker admitted new TCP')
  except (OSError,EOFError):pass
  except AssertionError as e:
   if 'drained worker admitted' in str(e):raise
 finally:p.HOST='127.0.0.1'
 reports.append('drain targets only worker-2 and blocks new traffic')
 f.operation('workers/worker-2/resume')
 until(lambda:not second_status()['drained'],10)
 traffic('127.0.0.2',62080)
 before=second_status();route=next(x for x in before['cores'] if x['ready']);pid=route['pid']
 f.operation('workers/worker-2/restart-core',{'route_id':route['profile_id']})
 until(lambda:any(x['profile_id']==route['profile_id'] and x['ready'] and x['pid']!=pid for x in second_status()['cores']),15)
 old.sendall(b'unchanged');assert p.exact(old,9)==b'unchanged';old.close()
 reports.append('restart on worker-2 preserves worker-1 TCP session')
 previous=p.HOST;p.HOST='127.0.0.2'
 try:f.udp_echo(62080);assert p.proxy_http(61081,auth=('lab-user','lab-pass'))[0]==200
 finally:p.HOST=previous
 reports.append('worker-2 HTTP auth and SOCKS UDP through independent published ports')
 result=f.operation('config/apply',timeout=210)
 workers=[w for w in f.request('workers')['items'] if w['online']]
 assert len(workers)==2 and len({w['generation'] for w in workers})==1
 reports.append('configuration apply waits for both enrolled workers')
 traffic('127.0.0.1',f.SOCKS);traffic('127.0.0.2',62080)
 report={'passed':True,'checks':reports,'workers':[{'id':w['id'],'generation':w['generation']} for w in workers]}
 Path('/reports/multiworker.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)

if __name__=='__main__':main()
