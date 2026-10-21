"""Real 10k synthetic routing catalogue, bounded observation, explicit restore."""
import json
import os
import sys
import time
from pathlib import Path
import full_acceptance as f
import acceptance as p
f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
policies=f.request('policies')['items']
assert len(policies)==2 and all(row['name'].startswith('Topology ') for row in policies)
mutated=False
report={'passed':False}
try:
    existing=f.request('sources?limit=1000')['items']
    by_name={row['name']:row for row in existing}
    scale_ids={row['id'] for row in existing if row['name'].startswith('Topology scale ')}
    base_count=0;base_sources=set();offset=0
    while True:
        page=f.request('profiles?limit=1000&offset='+str(offset))
        for row in page['items']:
            data=row['data']
            if data.get('present') and data.get('source_id') not in scale_ids:
                base_count+=1;base_sources.add(data['source_id'])
        offset+=len(page['items'])
        if offset>=page['total']:break
    assert 0<base_count<100 and all(row['name'].startswith('Topology ') for row in existing if row['id'] in base_sources)
    remaining=10000-base_count;sources=[];imported=0
    for index in range(5):
        count=min(2000,remaining);assert count>0
        data={'url':f'http://subscription:9004/scale?start={1+index*2000}&count={count}','allow_private':True,'core_version':'26.10.17','interval_seconds':0}
        name='Topology scale '+str(index)
        if name in by_name:source=f.patch(by_name[name],data)
        else:source=f.create('sources',name,data)
        f.operation('sources/'+source['id']+'/refresh',timeout=240);sources.append(source)
        remaining-=count;imported+=count
        print(json.dumps({'stage':'imported','sources':len(sources),'scale_profiles':imported,'existing_synthetic_profiles':base_count}),flush=True)
    assert remaining==0
    aggregate=f.create('aggregates','Topology scale 10000',{'source_ids':sorted(base_sources)+[s['id'] for s in sources],'dedupe':False})
    pool=f.create('pools','Topology scale pool',{'aggregate_id':aggregate['id']})
    assert f.request('pools/'+pool['id']+'/preview')['total']==10000
    mutated=True
    for policy in policies:f.patch(policy,{'pool_id':pool['id']})
    f.operation('config/apply',timeout=300)
    deadline=time.monotonic()+120
    while time.monotonic()<deadline:
        snapshot=f.request('topology/snapshot')
        if snapshot['workers'] and snapshot['workers'][0]['catalog_size']==10000 and snapshot['workers'][0]['routes']:break
        time.sleep(.5)
    else:raise AssertionError('real 10000 route generation not observed')
    for _ in range(20):assert p.proxy_http(f.HTTP,'/bytes?n=65536')[0]==200
    raw=json.dumps(snapshot).encode()
    assert len(snapshot['workers'][0]['routes'])<=128 and len(raw)<1024*1024
    report.update({'actual_routing_catalog':10000,'scale_profiles':imported,'existing_synthetic_profiles':base_count,'observed_engines':len(snapshot['workers'][0]['routes']),'snapshot_bytes':len(raw),'generation':snapshot['workers'][0]['generation'],'real_transfers':20})
    Path('/reports/topology-catalog-snapshot.json').write_text(json.dumps(snapshot,indent=2))
    print(json.dumps({'stage':'catalogue_ready',**report}),flush=True)
    if os.getenv('TOPOLOGY_SCALE_HOLD_FOR_BROWSER')=='true':assert sys.stdin.readline().strip()=='continue'
    report['passed']=True
finally:
    if mutated:
        for prior in policies:
            current=f.request('policies/'+prior['id'])
            data=dict(prior['data'],probe_url='http://origin:9000/healthz',probe_fallback_urls=[],speed_url='http://origin:9000/bytes?n=8388608')
            f.patch(current,data)
        f.operation('config/apply',timeout=300)
        deadline=time.monotonic()+120
        while time.monotonic()<deadline:
            after=f.request('topology/snapshot')
            if after['workers'] and after['workers'][0]['catalog_size']==3:break
            time.sleep(.5)
        else:raise AssertionError('original three-route lab not restored')
        report['restored_routing_catalog']=3
    Path('/reports/topology-catalog.json').write_text(json.dumps(report,indent=2))
print(json.dumps({'stage':'finished',**report}),flush=True)
