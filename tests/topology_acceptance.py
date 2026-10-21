"""Real synthetic Xray/control-plane topology acceptance; no provider material."""
import json
import socket
import time
import urllib.error
import urllib.request
from pathlib import Path
import full_acceptance as f
import acceptance as p

def wait_frame(predicate, timeout=60):
    until=time.monotonic()+timeout
    while time.monotonic()<until:
        snapshot=f.request('topology/snapshot')
        if predicate(snapshot):return snapshot
        time.sleep(.3)
    raise AssertionError('topology observation timeout')

def sse(cursor, headers=None):
    request=urllib.request.Request(f.BASE+'topology/events?cursor='+cursor)
    for key,value in (headers or {}).items():request.add_header(key,value)
    return f.client.open(request,timeout=15)

def event(stream):
    result={}
    while True:
        line=stream.readline().decode().strip()
        if not line and result:return result
        if not line:continue
        key,_,value=line.partition(': ')
        result[key]=value

def provision():
    existing=f.request('listeners')['items']
    if existing:
        assert all(item['name'].startswith('Topology ') for item in existing),'foreign fixture in owned project'
        primary=next(item for item in existing if item['data']['listen']=='0.0.0.0:8080')
        policy=f.request('policies/'+primary['data']['policy_id'])
        pool=f.request('pools/'+policy['data']['pool_id'])
        aggregate=f.request('aggregates/'+pool['data']['aggregate_id'])
        sources=[f.request('sources/'+source_id) for source_id in aggregate['data']['source_ids']]
        return policy,sources
    sources=[]
    for i,upstream in enumerate(['shaper-a','shaper-a','shaper-b']):
        source=f.create('sources','Topology synthetic '+str(i),{'url':'http://subscription:9004/subscription?upstream='+upstream,'allow_private':True,'core_version':'26.10.17','interval_seconds':0})
        f.operation('sources/'+source['id']+'/refresh')
        sources.append(source)
    aggregate=f.create('aggregates','Topology all',{'source_ids':[s['id'] for s in sources],'dedupe':False})
    pool=f.create('pools','Topology pool',{'aggregate_id':aggregate['id']})
    policy_data={'pool_id':pool['id'],'mode':'throughput','active_paths':3,'warm_reserve':0,'probe_url':'http://origin:9000/healthz','speed_url':'http://origin:9000/bytes?n=8388608','probe_interval_seconds':30,'max_connections':5000}
    policy=f.create('policies','Topology policy',policy_data)
    observation_policy=f.create('policies','Topology observation policy',policy_data)
    for name,proto,port in [('Topology HTTP','http',f.HTTP),('Topology SOCKS','socks5',f.SOCKS)]:
        f.create('listeners',name,{'protocol':proto,'policy_id':policy['id'],'udp':proto=='socks5'})
    f.create('listeners','Topology observation HTTP',{'protocol':'http','listen':'0.0.0.0:'+str(f.HTTP_AUTH),'policy_id':observation_policy['id'],'udp':False})
    return policy,sources

def main():
    for path in ['snapshot','events','history','decisions/absent']:
        f.request('topology/'+path,expected=401)
    f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
    f.check('topology endpoints authenticated',lambda:None)
    settings=f.request('settings/global')
    f.patch(settings,{'udp_port_min':32000,'udp_port_max':32063})
    policy,sources=provision()
    f.check('apply synthetic catalog over mTLS',lambda:f.operation('config/apply',timeout=240))
    initial=wait_frame(lambda s:len(s['workers'])==1 and len(s['workers'][0]['routes'])>=3)
    routes=initial['workers'][0]['routes']
    assert len({r['engine'] for r in routes})>=3
    f.check('actual engines and applied generation',lambda:f.assertion(initial['workers'][0]['generation']>0))
    # Refresh is performed by the existing runtime DNS subsystem, not by topology.
    grouped=wait_frame(lambda s:len({r['group'] for r in s['workers'][0]['routes']})==2,timeout=90)
    f.check('observes shared DNS IP across subscriptions and independent IP',lambda:f.assertion(sorted(len([r for r in grouped['workers'][0]['routes'] if r['group']==group]) for group in {r['group'] for r in grouped['workers'][0]['routes']})==[1,2]))
    baseline={r['engine']:(r['up'],r['down']) for r in grouped['workers'][0]['routes']}
    for i in range(40):
        assert p.proxy_http(f.HTTP,'/bytes?n=65536')[0]==200
        assert p.socks_http(f.SOCKS,'/bytes?n=65536')[0]==200
    f.check('HTTP and SOCKS5 payload traffic',lambda:None)
    f.check('SOCKS5 UDP real payload',lambda:f.udp_echo(f.SOCKS))
    f.check('CONNECT real TLS',lambda:f.connect_tls())
    measured=wait_frame(lambda s:sum(r['down']-baseline[r['engine']][1] for r in s['workers'][0]['routes'])>=80*65536)
    f.check('download direction matches controlled payload lower bound',lambda:None)
    status=p.json_url(f.STATUS_URL)
    # Snapshot/status are separate instants. Quiescent totals must converge exactly.
    equal=wait_frame(lambda s:sum(r['down'] for r in s['workers'][0]['routes'])==status['payload_bytes_down'] and sum(r['up'] for r in s['workers'][0]['routes'])==status['payload_bytes_up'])
    f.check('no double count against existing worker counters',lambda:None)
    with sse(equal['cursor']) as stream:
        e=event(stream);assert e['event']=='frame';assert int(e['id'].split(':')[1])==int(equal['cursor'].split(':')[1])+1
        frame=json.loads(e['data'])['frame']
    f.check('atomic snapshot then contiguous authenticated SSE',lambda:None)
    decisions=[d for worker in measured['workers'] for d in worker['decisions']]
    # Collect another decision batch if the latest frame happened to be empty.
    if not decisions:
        for _ in range(32):p.proxy_http(f.HTTP)
        measured=wait_frame(lambda s:any(w['decisions'] for w in s['workers']))
        decisions=[d for w in measured['workers'] for d in w['decisions']]
    for d in decisions:
        saved=f.request('topology/decisions/'+d['id']);assert saved['generation']>0 and saved['method']
    f.check('persisted actual sampled decision evidence',lambda:None)
    with socket.create_connection(('127.0.0.1',f.HTTP),timeout=10) as sock:
        sock.sendall(b'CONNECT origin:9000 HTTP/1.1\r\nHost: origin:9000\r\n\r\n')
        reply=sock.recv(4096)
        if b' 200 ' in reply:
            sock.sendall(b'GET /transport-close HTTP/1.1\r\nHost: origin\r\nConnection: close\r\n\r\n')
            try:assert sock.recv(4096)==b''
            except ConnectionResetError:pass
        else:assert b' 502 ' in reply
    failed=wait_frame(lambda s:any(d['reason'] in ('target_connect_failed','early_transport_close') for w in s['workers'] for d in w['decisions']))
    assert all(not d.get('selected') for w in failed['workers'] for d in w['decisions'] if d['reason'] in ('target_connect_failed','early_transport_close'))
    f.check('real failed target reports transport fact without invented application cause',lambda:None)
    with sse('obsolete:0',{'Last-Event-ID':f.request('topology/snapshot')['cursor']}) as stream:assert event(stream)['event']=='frame'
    f.check('Last-Event-ID overrides query cursor',lambda:None)
    streams=[]
    try:
        for _ in range(4):streams.append(sse(f.request('topology/snapshot')['cursor']))
        f.request('topology/events?cursor='+f.request('topology/snapshot')['cursor'],expected=429)
    finally:
        for stream in streams:stream.close()
    time.sleep(1)
    f.check('bounded concurrent live subscribers',lambda:None)
    with sse('obsolete:0') as stream:assert event(stream)['event']=='resync'
    f.check('epoch loss requires resnapshot',lambda:None)
    f.request('topology/decisions/absent',expected=410)
    f.request('topology/snapshot','POST',{},expected=405)
    f.request('topology/snapshot?q='+'x'*300,expected=400)
    f.check('expired IDs read-only and query bounds',lambda:None)
    history=f.request('topology/history');assert history['items']
    raw=json.dumps(measured)
    for forbidden in ['lab-pass','00000000-0000-4000-8000-000000000001','subscription?','certificateFile','outbound','authorization','cookie','http://']:
        assert forbidden.lower() not in raw.lower(),forbidden
    f.check('privacy allowlist excludes credentials URLs bodies client destinations',lambda:None)
    Path('/reports/topology-snapshot.json').write_text(json.dumps(measured,indent=2))
    Path('/reports/topology-ids.json').write_text(json.dumps({'policy':policy['id'],'sources':[s['id'] for s in sources]}))
    Path('/reports/topology-acceptance.json').write_text(json.dumps({'passed':True,'checks':f.records},indent=2))
    print(json.dumps({'passed':True,'checks':len(f.records)}),flush=True)

if __name__=='__main__':main()
