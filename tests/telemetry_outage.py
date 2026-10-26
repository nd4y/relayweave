"""Actual observability outage leaves forwarding live; only own test services stop."""
import json
import os
from pathlib import Path
import re
import subprocess
import time
import urllib.request
import urllib.error
import urllib.parse

root=Path('/home/av/build/relayweave')
for line in (root/'integration.env').read_text().splitlines():
 key,_,value=line.partition('=')
 if key=='ADMIN_PASSWORD':os.environ[key]=value
os.environ.update(HTTP_PORT='48080',HTTP_AUTH_PORT='48081',SOCKS_PORT='41080',SOCKS_AUTH_PORT='41081')
import full_acceptance as f
import acceptance as p
from advanced_acceptance import echo

names=['relayweave-'+name+'-1' for name in ('prometheus','grafana','loki')]
def docker(*args):return subprocess.check_output(['docker',*args],timeout=90).decode()
def dropped():
 with urllib.request.urlopen('http://127.0.0.1:19090/metrics',timeout=5) as response:raw=response.read()
 return int(re.search(rb'^proxy_telemetry_dropped_total (\d+)$',raw,re.M)[1])

def main():
 for name in names:
  obj=json.loads(docker('inspect',name))[0];labels=obj['Config']['Labels']
  assert labels['com.docker.compose.project']=='relayweave' and labels['com.docker.compose.service'] in ('prometheus','grafana','loki') and obj['State']['Running']
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 stream,_=p.socks_connect(f.SOCKS_AUTH,target_port=9001,auth=('lab-user','lab-pass'))
 before=dropped();cycles=0;started=time.monotonic();failure=None;unavailable_browser_passed=False
 try:
  docker('stop','-t','10',*names)
  # Exercise the actual nginx error path, not a mocked browser response.
  monitoring_url=urllib.parse.urljoin(f.BASE,'/grafana/')
  try:
   with f.client.open(monitoring_url,timeout=8) as response:status=response.status;body=response.read(32768);headers=response.headers
  except urllib.error.HTTPError as exc:status=exc.code;body=exc.read(32768);headers=exc.headers
  assert status==503 and headers.get('Retry-After')=='5',(status,headers.get('Retry-After'))
  assert b'data-rw-observability-unavailable' in body and b'502 Bad Gateway' not in body
  browser_command=['docker','compose','--env-file',str(root/'integration.env'),'-f','compose.yaml','-f','compose.integration.yaml','-f','compose.build01-browser.yaml','--profile','test','run','--rm','browser','node','tests/grafana-unavailable.mjs']
  with (root/'reports'/(Path.cwd().name+'-monitoring-outage-browser.log')).open('w') as output:
   subprocess.run(browser_command,stdout=output,stderr=subprocess.STDOUT,check=True,timeout=90)
  unavailable_browser_passed=True
  # Emits real bounded application logs while Loki cannot accept exports.
  f.operation('config/apply')
  outage=time.monotonic()
  while time.monotonic()-outage<60:
   assert p.proxy_http(f.HTTP)[0]==200 and p.socks_http(f.SOCKS)[0]==200
   assert p.proxy_http(f.HTTP_AUTH,auth=('lab-user','lab-pass'))[0]==200
   f.udp_echo(f.SOCKS);f.udp_echo(f.SOCKS_AUTH,('lab-user','lab-pass'))
   echo(stream,b'telemetry-independent-data');cycles+=1;time.sleep(2)
  after=dropped();assert after>before,(before,after)
 except BaseException as exc:failure=repr(exc);raise
 finally:
  stream.close();docker('start',*names)
  Path('reports/telemetry-outage.json').write_text(json.dumps({'passed':failure is None,'error':failure,'traffic_cycles':cycles,'elapsed_seconds':time.monotonic()-started,'dropped_logs_before':before,'dropped_logs_after':locals().get('after'),'monitoring_unavailable_http_status':locals().get('status'),'monitoring_unavailable_browser_passed':unavailable_browser_passed,'scope':'Prometheus/Grafana/Loki stopped for at least60actualseconds; actual desktop/mobile unavailable page, retry and return; existing and new HTTP/SOCKS TCP/UDP continue. Only labelled own Compose services changed.'},indent=2))
 print(json.dumps({'passed':True,'cycles':cycles,'log_export_drops':after-before}),flush=True)

if __name__=='__main__':main()
