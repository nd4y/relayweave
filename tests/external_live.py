"""Bounded real-provider test. Never prints URLs, names, headers, UUIDs or raw errors."""
import json
import os
import socket
import struct
import time
import re
from pathlib import Path
import full_acceptance as f
import acceptance as p

stage='initializing'
def safe_failure(error):
 code=type(error).__name__
 if isinstance(error,AssertionError) and error.args:
  detail=error.args[0]
  if isinstance(detail,dict):code=detail.get('data',{}).get('error',code)
  elif isinstance(detail,tuple) and len(detail)>=4 and isinstance(detail[3],bytes):
   try:code=json.loads(detail[3]).get('error',{}).get('code',code)
   except (ValueError,AttributeError):pass
 return code if re.fullmatch('[a-zA-Z0-9_]{1,80}',str(code)) else type(error).__name__

def create(kind,name,data):
 found=next((v for v in f.request(kind+'?limit=1000')['items'] if v['name']==name),None)
 return found or f.create(kind,name,data)

def dns(port):
 control,bound=p.socks_connect(port,command=3,target='0.0.0.0',target_port=0)
 with control,socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as udp:
  udp.settimeout(5);transaction=os.urandom(2)
  query=transaction+b'\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00'+b'\x07example\x03com\x00\x00\x01\x00\x01'
  udp.sendto(b'\0\0\0'+p.address('1.1.1.1',53)+query,bound)
  reply=p.datagram_payload(udp.recv(4096));assert reply[:2]==transaction and reply[2]&0x80 and reply[3]&15==0

def main():
 global stage
 f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
 existing=f.request('sources')['items']
 assert all(s['name'] in ['Provider '+str(n) for n in range(1,5)] for s in existing),'Dedicated external data root required'
 settings=f.request('settings/global');f.patch(settings,{'udp_port_min':33000,'udp_port_max':33255})
 records=[];sources=[];policies=[]
 for n,row in enumerate(json.loads(Path('/private/sources.json').read_text()),1):
  headers={}
  if row.get('upstream_ua'):headers['User-Agent']=row['upstream_ua']
  if row.get('hwid') and row.get('hwid_mode') not in (False,0,'off','disabled','none'):
   for field,key in [('hwid','x-hwid'),('device_os','x-device-os'),('device_ver','x-ver-os'),('device_model','x-device-model')]:
    if row.get(field):headers[key]=str(row[field])
  stage='import_provider_'+str(n)
  source=create('sources','Provider '+str(n),{'url':row['upstream_url'],'headers':headers,'interval_seconds':0,'core_version':'26.10.17'})
  try:
   if not any(s['id']==source['id'] for s in existing):f.operation('sources/'+source['id']+'/refresh',timeout=180)
   source=f.request('sources/'+source['id']);sources.append(source)
   profiles=[x for x in f.request('profiles?limit=1000')['items'] if x['data']['source_id']==source['id']]
   record={'provider':n,'imported':True,'profiles':len(profiles),'supported':sum(x['data']['supported'] for x in profiles),'protocols':sorted({x['data']['protocol'] for x in profiles}),'hwid_forwarded':bool(headers.get('x-hwid'))}
  except Exception:
   record={'provider':n,'imported':False,'error':'source_refresh_failed'}
  records.append(record);print(json.dumps(record),flush=True)
 assert sources,'No authorized source could be imported'
 stage='aggregate_preview'
 aggregate=create('aggregates','All authorized providers',{'source_ids':[s['id'] for s in sources],'dedupe':True})
 fullpool=create('pools','All providers',{'aggregate_id':aggregate['id']});assert f.request('pools/'+fullpool['id']+'/preview')['total']>0
 for index,source in enumerate(sources):
  stage='policy_'+str(index+1)
  pool=create('pools','Provider pool '+str(index+1),{'source_ids':[source['id']]})
  policy=create('policies','Provider policy '+str(index+1),{'pool_id':pool['id'],'mode':'throughput','active_paths':1,'warm_reserve':0,'probe_url':'https://www.gstatic.com/generate_204','probe_fallback_urls':['https://cp.cloudflare.com/generate_204'],'probe_interval_seconds':30,'speed_url':'','probe_budget_bytes':128<<20})
  policies.append(policy)
 stage='listeners'
 listener_http=create('listeners','External HTTP',{'protocol':'http','listen':'0.0.0.0:8080','udp':False,'policy_id':policies[0]['id']})
 listener_socks=create('listeners','External SOCKS',{'protocol':'socks5','listen':'0.0.0.0:1080','udp':True,'policy_id':policies[0]['id']})
 for index,(source,policy) in enumerate(zip(sources,policies)):
  record=next(r for r in records if r['provider']==int(source['name'].split()[-1]))
  try:
   for listener in [listener_http,listener_socks]:
    current=f.request('listeners/'+listener['id']);f.patch(current,{'policy_id':policy['id']})
   f.operation('config/apply',timeout=210)
   status=p.json_url(f.STATUS_URL);routes=[c for c in status['cores'] if c['ready']]
   assert routes
   # /test uses the selected profile's actual engine and a real external URL.
   selected=next(c for c in routes if any(x['id']==c['profile_id'] and x['data']['source_id']==source['id'] for x in f.request('profiles?limit=1000')['items']))
   probe=f.operation('profiles/'+selected['profile_id']+'/test')
   record.update(healthy=bool(probe['healthy']),ttfb_ms=probe['ttfb_ms'])
   policy=f.request('policies/'+policy['id']);f.patch(policy,{'speed_url':'https://speed.cloudflare.com/__down?bytes=8388608'})
   f.operation('config/apply',timeout=210)
   speed=f.operation('profiles/'+selected['profile_id']+'/speed-test')
   record.update(sample_bytes=speed['bytes'],sample_mbps=speed['mbps'])
   policy=f.request('policies/'+policy['id']);f.patch(policy,{'speed_url':''})
   try:dns(f.SOCKS);record['udp_dns']=True
   except Exception:record['udp_dns']=False
  except Exception as error:
   record['traffic_error']=safe_failure(error)
  print(json.dumps(record),flush=True)
  Path('/reports/external-live.json').write_text(json.dumps({'complete':False,'providers':records},indent=2))
 # Quiesce real-provider checks, regardless of individual provider availability.
 for listener in [listener_http,listener_socks]:
  current=f.request('listeners/'+listener['id']);f.patch(current,{'enabled':False})
 f.operation('config/apply',timeout=210)
 status=p.json_url(f.STATUS_URL);assert status['probe_bytes']<=128<<20
 report={'complete':True,'passed':all(r.get('imported') and r.get('healthy') and r.get('sample_mbps',0)>0 for r in records),'providers':records,'probe_body_bytes':status['probe_bytes'],'scope':'Authorized existing sources read-only; no provider identity or credential in report. 8MiB/3s active samples and tiny UDP DNS checks; 128MiB worker daily body budget. WAN observations, not a guaranteed home-link speed.'}
 Path('/reports/external-live.json').write_text(json.dumps(report,indent=2));print(json.dumps({'complete':True,'passed':report['passed'],'providers':len(records),'probe_body_bytes':status['probe_bytes']}),flush=True)

if __name__=='__main__':
 try:main()
 except Exception as error:print(json.dumps({'interrupted':True,'stage':stage,'code':safe_failure(error)}),flush=True);raise SystemExit(1)
