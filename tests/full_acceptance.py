"""Control plane + real Xray + published HTTP/SOCKS5 TCP/UDP. No provider secrets."""
import base64
import concurrent.futures
import http.cookiejar
import json
import os
import socket
import time
import urllib.error
import urllib.request
from pathlib import Path
import acceptance as protocol

BASE=os.environ.get('API_URL','http://127.0.0.1:18443/api/v1/')
PASSWORD=os.environ['ADMIN_PASSWORD']
HTTP=int(os.environ.get('HTTP_PORT',18080))
HTTP_AUTH=int(os.environ.get('HTTP_AUTH_PORT',18081))
SOCKS=int(os.environ.get('SOCKS_PORT',11080))
SOCKS_AUTH=int(os.environ.get('SOCKS_AUTH_PORT',11081))
REPORT_PREFIX=os.environ.get('REPORT_PREFIX','')
STATUS_URL=os.environ.get('WORKER_STATUS_URL','http://127.0.0.1:19090/status')
csrf=''
jar=http.cookiejar.CookieJar()
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPCookieProcessor(jar))
records=[]

def request(path,method='GET',body=None,version=None,expected=200,headers=None):
    data=json.dumps(body).encode() if body is not None else None
    h={'Content-Type':'application/json','X-CSRF-Token':csrf}
    if version is not None:h['If-Match']=str(version)
    h.update(headers or {})
    req=urllib.request.Request(BASE+path,data=data,method=method,headers=h)
    try:
        with client.open(req,timeout=35) as response:
            status=response.status
            raw=response.read()
    except urllib.error.HTTPError as e:status=e.code;raw=e.read()
    assert status==expected,(method,path,status,raw[:400])
    return json.loads(raw) if raw else None

def create(kind,name,data):return request(kind,'POST',{'name':name,'data':data},expected=201)
def patch(item,data):return request(item['kind']+'/'+item['id'],'PATCH',{'data':data},item['version'])
def operation(path,body=None,timeout=190):
    op=request(path,'POST',body or {},expected=202)
    return wait_operation(op,timeout)
def wait_operation(op,timeout=190):
    deadline=time.monotonic()+timeout
    while time.monotonic()<deadline:
        current=request('operations/'+op['id'])
        if current['data']['status'] in ('succeeded','failed','cancelled'):
            assert current['data']['status']=='succeeded',current
            return current['data'].get('result')
        time.sleep(.3)
    raise TimeoutError(op['id'])
def check(name,fn):
    started=time.monotonic()
    try:result=fn();records.append({'name':name,'passed':True,'seconds':round(time.monotonic()-started,3)});print('PASS',name,flush=True);return result
    except Exception as e:records.append({'name':name,'passed':False,'error':str(e)[:2000]});raise

def main():
    global csrf
    request('overview',expected=401)
    request('auth/login','POST',{'password':'incorrect'},expected=401)
    login=request('auth/login','POST',{'password':PASSWORD});csrf=login['csrf']
    check('password-only authentication',lambda:request('auth/session'))
    if os.environ.get('UDP_PORT_MIN'):
        settings=request('settings/global');patch(settings,{'udp_port_min':int(os.environ['UDP_PORT_MIN']),'udp_port_max':int(os.environ['UDP_PORT_MAX'])})
    check('CSRF rejection',lambda:request('sources','POST',{'name':'bad','data':{}},expected=403,headers={'X-CSRF-Token':'wrong'}))
    check('cross-origin mutation rejected',lambda:request('sources','POST',{},expected=403,headers={'Origin':'https://invalid.example'}))
    # A new dedicated DATA_ROOT makes this repeatable without deleting anyone's stack.
    sources=request('sources')['items']
    assert not sources,'Use a clean dedicated integration data directory'
    src=check('create primary subscription',lambda:create('sources','Lab primary',{'url':'http://subscription:9004/subscription','allow_private':True,'core_version':'26.10.17','interval_seconds':0}))
    check('source secrets redacted',lambda:assertion(request('sources/'+src['id'])['data']['url']==''))
    check('refresh and parse Xray inline CA',lambda:operation('sources/'+src['id']+'/refresh'))
    src=request('sources/'+src['id'])
    check('CAS conflict rejected',lambda:request('sources/'+src['id'],'PATCH',{'data':{'enabled':False}},src['version']-1,expected=409))
    second=check('create secondary subscription',lambda:create('sources','Lab secondary',{'url':'http://subscription:9004/subscription','allow_private':True,'core_version':'26.9.30','interval_seconds':0}))
    operation('sources/'+second['id']+'/refresh')
    profiles=request('profiles')['items'];assert len(profiles)==2
    check('canonical semantic fingerprint',lambda:assertion(profiles[0]['data']['fingerprint']==profiles[1]['data']['fingerprint']))
    check('profile credentials absent from GET',lambda:assertion(all('outbound' not in p['data'] for p in profiles)))
    agg=create('aggregates','Lab aggregate',{'source_ids':[src['id'],second['id']],'dedupe':True,'prefix_names':True})
    pool=create('pools','Lab deduped',{'aggregate_id':agg['id']})
    check('aggregate deduplicates credentials-sensitive profiles',lambda:assertion(request('pools/'+pool['id']+'/preview')['total']==1))
    # Separate pools force both actual core versions into the running data plane.
    pool_a=create('pools','Primary pool',{'source_ids':[src['id']]})
    pool_b=create('pools','Secondary pool',{'source_ids':[second['id']]})
    policy_data={'active_paths':1,'warm_reserve':0,'probe_url':'http://origin:9000/healthz','probe_fallback_urls':[], 'speed_url':'http://origin:9000/bytes?n=8388608','probe_interval_seconds':10,'max_connections':5000}
    policy_a=create('policies','Primary policy',dict(policy_data,pool_id=pool_a['id']))
    policy_b=create('policies','Secondary policy',dict(policy_data,pool_id=pool_b['id']))
    for name,proto,port,policy,auth in [('HTTP primary','http',8080,policy_a,False),('SOCKS primary','socks5',1080,policy_a,False),('HTTP secondary auth','http',8081,policy_b,True),('SOCKS secondary auth','socks5',1081,policy_b,True)]:
        data={'protocol':proto,'listen':f'0.0.0.0:{port}','policy_id':policy['id'],'udp':proto=='socks5'}
        if auth:data.update(username='lab-user',password='lab-pass')
        create('listeners',name,data)
    check('configuration plan',lambda:request('config/plan','POST',{}))
    for attempt in range(2):
        policy_a=request('policies/'+policy_a['id'])
        check('initial rejected rollout %d recovers to an explicit empty revision'%(attempt+1),lambda:first_rejected_rollout(policy_a,src))
    policy_a=request('policies/'+policy_a['id'])
    check('prepare, canary and apply through mTLS',lambda:operation('config/apply',timeout=210))
    time.sleep(2)
    check('HTTP primary real VLESS TLS',lambda:assertion(protocol.proxy_http(HTTP)[0]==200))
    check('HTTP secondary authenticated',lambda:assertion(protocol.proxy_http(HTTP_AUTH,auth=('lab-user','lab-pass'))[0]==200))
    check('HTTP authentication enforced',lambda:assertion(protocol.proxy_http(HTTP_AUTH)[0]==407))
    check('SOCKS primary TCP',lambda:assertion(protocol.socks_http(SOCKS)[0]==200))
    check('SOCKS secondary authenticated TCP',lambda:assertion(protocol.socks_http(SOCKS_AUTH,auth=('lab-user','lab-pass'))[0]==200))
    check('SOCKS UDP primary NAT',lambda:udp_echo(SOCKS))
    check('SOCKS UDP secondary authenticated NAT',lambda:udp_echo(SOCKS_AUTH,('lab-user','lab-pass')))
    check('HTTP CONNECT TLS',lambda:connect_tls())
    status=protocol.json_url(STATUS_URL)
    if os.environ.get('EXPECT_ARCH'):check('actual worker target architecture',lambda:assertion(status['architecture']==os.environ['EXPECT_ARCH']))
    check('two exact Xray versions ready',lambda:assertion({x['version'] for x in status['cores'] if x['ready']}=={'26.10.17','26.9.30'}))
    primary=next(p for p in profiles if p['data']['source_id']==src['id'])
    check('active profile probe',lambda:operation('profiles/'+primary['id']+'/test'))
    speed=check('bounded active speed sample',lambda:operation('profiles/'+primary['id']+'/speed-test'))
    assert speed['bytes']==8388608 and speed['mbps']>0
    check('dependency deletion rejected',lambda:request('policies/'+policy_a['id'],'DELETE',version=policy_a['version'],expected=409))
    bad=create('sources','Malformed source',{'url':'http://subscription:9004/html','allow_private':True,'interval_seconds':0})
    failed=request('sources/'+bad['id']+'/refresh','POST',{},expected=202)
    time.sleep(2);check('HTML does not wipe catalog',lambda:assertion(request('operations/'+failed['id'])['data']['status']=='failed' and len(request('profiles')['items'])==2))
    bad=request('sources/'+bad['id']);request('sources/'+bad['id'],'DELETE',version=bad['version'])
    trash=request('trash')['items'];entry=next(x for x in trash if x['name']=='Malformed source')
    check('atomic undo',lambda:request('trash/'+entry['id']+'/restore','POST',{}))
    request('openapi.json')
    report={'passed':all(x['passed'] for x in records),'checks':records,'speed_sample':speed,'versions':sorted({x['version'] for x in status['cores']})}
    Path('/reports/'+REPORT_PREFIX+'full-acceptance.json').write_text(json.dumps(report,indent=2))
    Path('/reports/'+REPORT_PREFIX+'integration-ids.json').write_text(json.dumps({'source_primary':src['id'],'source_secondary':second['id'],'policy_primary':policy_a['id'],'policy_secondary':policy_b['id'],'profile_primary':primary['id']}))
    print(json.dumps({'passed':report['passed'],'checks':len(records)}),flush=True)

def assertion(value):assert value
def first_rejected_rollout(policy,source):
    # GET deliberately masks URL paths. Restore the known fixture value rather
    # than writing that redacted display value back as a different probe URL.
    original_probe='http://origin:9000/healthz'
    patch(policy,{'probe_url':'http://origin:9000/missing-initial-canary','probe_fallback_urls':[]})
    op=request('config/apply','POST',{},expected=202)
    deadline=time.monotonic()+210;current=None
    while time.monotonic()<deadline:
        current=request('operations/'+op['id'])
        if current['data']['status'] in ('succeeded','failed','cancelled'):break
        time.sleep(.3)
    assert current and current['data']['status']=='failed' and current['data']['error']=='worker_validation_failed',current
    failed_generation=max(x['generation'] for x in request('config')['items'] if x['status']=='failed')
    deadline=time.monotonic()+60;recovery=None;workers=[]
    while time.monotonic()<deadline:
        recovery=next((x for x in request('config')['items'] if x['status']=='recovering' and x['generation']>failed_generation),None)
        workers=request('overview')['workers']
        if recovery and any(x['generation']>=recovery['generation'] for x in workers):break
        time.sleep(.3)
    assert recovery and any(x['generation']>=recovery['generation'] for x in workers),'Worker never received a higher empty recovery revision'
    def empty_runtime():
        state=protocol.json_url(STATUS_URL)
        assert state['catalog_size']==0 and not state['policies'],state
        probes=[lambda:protocol.proxy_http(HTTP),lambda:protocol.proxy_http(HTTP_AUTH,auth=('lab-user','lab-pass')),lambda:protocol.socks_http(SOCKS),lambda:protocol.socks_http(SOCKS_AUTH,auth=('lab-user','lab-pass'))]
        for probe in probes:
            try:
                status=probe()[0]
            except Exception:continue
            assert status!=200,'A first failed rollout still forwards business traffic'
    empty_runtime()
    current=request('policies/'+policy['id']);patch(current,{'probe_url':original_probe})
    for enabled in (False,True):
        before=max(x['generation'] for x in request('overview')['workers'])
        current=request('sources/'+source['id']);patch(current,{'enabled':enabled})
        deadline=time.monotonic()+60;advanced=False
        while time.monotonic()<deadline:
            empty_runtime()
            if any(x['generation']>before for x in request('overview')['workers']):advanced=True;break
            time.sleep(.3)
        assert advanced,'Operational source change did not exercise automatic reconciliation'
    empty_runtime()
    check('source reconciliation cannot activate a first failed blueprint',lambda:assertion(protocol.json_url(STATUS_URL)['catalog_size']==0))

def udp_echo(port,auth=None):
    control,bound=protocol.socks_connect(port,command=3,target='0.0.0.0',target_port=0,auth=auth)
    with control,socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as udp:
        udp.settimeout(5);payload=b'full-compose-udp-test';udp.sendto(b'\0\0\0'+protocol.address('origin',9002)+payload,bound);reply,_=udp.recvfrom(8192);assert reply.endswith(payload)
def connect_tls():
    import ssl
    raw=socket.create_connection(('127.0.0.1',HTTP),timeout=10)
    raw.sendall(b'CONNECT origin:9443 HTTP/1.1\r\nHost: origin:9443\r\n\r\n');buf=b''
    while b'\r\n\r\n' not in buf:buf+=raw.recv(1)
    assert buf.startswith(b'HTTP/1.1 200')
    context=ssl.create_default_context(cafile='/lab/ca.pem')
    with context.wrap_socket(raw,server_hostname='origin') as secure:
        secure.sendall(b'GET /healthz HTTP/1.1\r\nHost: origin\r\nConnection: close\r\n\r\n');assert protocol.response(secure).status==200
if __name__=='__main__':
    try:main()
    except BaseException:
        Path('/reports/'+REPORT_PREFIX+'full-acceptance-failed.json').write_text(json.dumps({'passed':False,'checks':records},indent=2));raise
