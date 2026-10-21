"""50 sources and 10,000 actual normalized, working VLESS/TLS profiles."""
import json
import math
import time
from pathlib import Path
import full_acceptance as f
import acceptance as p

def percentile(values,q=.95):return sorted(values)[math.ceil(len(values)*q)-1]
def timed(fn,count=30):
    for _ in range(3):fn()
    samples=[]
    for _ in range(count):
        start=time.perf_counter();fn();samples.append((time.perf_counter()-start)*1000)
    return {'p50_ms':percentile(samples,.5),'p95_ms':percentile(samples),'raw_ms':samples}
def main():
    f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
    sources=f.request('sources?limit=1000')['items'];assert not sources,'Use a fresh dedicated scale data root'
    settings=f.request('settings/global');f.patch(settings,{'udp_port_min':32000,'udp_port_max':32255})
    sources=[];start=time.monotonic()
    for i in range(50):
        source=f.create('sources',f'Scale {i+1:02d}',{'url':f'http://subscription:9004/scale?start={i*200+1}&count=200','allow_private':True,'interval_seconds':0,'core_version':'26.10.17'})
        result=f.operation('sources/'+source['id']+'/refresh');sources.append(source)
        assert f.request('sources/'+source['id'])['data']['profile_count']==200
        if (i+1)%10==0:print(f'Loaded {(i+1)*200} profiles / {i+1} sources',flush=True)
    assert f.request('profiles?limit=1')['total']==10000
    f.request('sources','POST',{'name':'Over budget','data':{'url':'http://subscription:9004/subscription','allow_private':True}},expected=409)
    aggregate=f.create('aggregates','All 50 sources',{'source_ids':[s['id'] for s in sources],'dedupe':True})
    pool=f.create('pools','10,000 eligible profiles',{'aggregate_id':aggregate['id']})
    preview=f.request('pools/'+pool['id']+'/preview');assert preview['total']==10000,preview
    policy=f.create('policies','Scale policy',{'pool_id':pool['id'],'active_paths':2,'warm_reserve':0,'probe_url':'http://origin:9000/healthz','probe_fallback_urls':[],'probe_interval_seconds':30})
    for protocol,port in [('http',8080),('socks5',1080)]:f.create('listeners','Scale '+protocol,{'protocol':protocol,'listen':f'0.0.0.0:{port}','policy_id':policy['id'],'udp':protocol=='socks5'})
    plan=f.request('config/plan','POST',{});assert plan['routes']==10000,plan
    f.operation('config/apply',timeout=240)
    assert p.proxy_http(f.HTTP)[0]==200;assert p.socks_http(f.SOCKS)[0]==200;f.udp_echo(f.SOCKS)
    status=p.json_url(f.STATUS_URL);assert status['catalog_size']==10000 and len(status['cores'])<=32
    results={}
    for name,path in [('overview','overview'),('profiles_1000','profiles?limit=1000&offset=4000'),('source','sources/'+sources[0]['id']),('pool_preview','pools/'+pool['id']+'/preview')]:
        results[name]=timed(lambda path=path:f.request(path));print(name,results[name]['p95_ms'],flush=True)
    settings=f.request('settings/global')
    def draft_write():
        nonlocal settings
        settings=f.patch(settings,{'download_mbps':800})
    results['draft_write']=timed(draft_write)
    passed=all(v['p95_ms']<=(500 if k=='draft_write' else 300) for k,v in results.items())
    report={'passed':passed,'sources':50,'profiles':10000,'runtime_catalog':status['catalog_size'],'processes_after_apply':len(status['cores']),'elapsed_seconds':round(time.monotonic()-start,3),'latency':results,'scope':'AMD64 reference control plane 1.5 CPU/768MiB, worker4CPU/2GiB; real parser/encrypted PostgreSQL/HTTP API/gRPC/Xray.'}
    Path('/reports/scale.json').write_text(json.dumps(report,indent=2));print(json.dumps({'passed':passed,'sources':50,'profiles':10000,'latency_p95_ms':{k:v['p95_ms'] for k,v in results.items()}}),flush=True);assert passed
if __name__=='__main__':main()
