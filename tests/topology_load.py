"""Bounded 5000 actual CONNECT tunnels, sustained echo, SSE delay and memory.
Run in the isolated topology client under the shared RelayWeave build lock.
"""
import asyncio
import datetime
import json
import os
import statistics
import threading
import time
import urllib.request
from pathlib import Path
import full_acceptance as f

COUNT=int(os.getenv('TOPOLOGY_LOAD_CONNECTIONS','5000'))
SECONDS=int(os.getenv('TOPOLOGY_LOAD_SECONDS','1800'))
assert 1<=COUNT<=5000 and 30<=SECONDS<=1800
latencies=[]
stop=threading.Event()

def live():
    cursor=f.request('topology/snapshot')['cursor']
    request=urllib.request.Request(f.BASE+'topology/events?cursor='+cursor)
    with f.client.open(request,timeout=20) as stream:
        while not stop.is_set():
            line=stream.readline().decode().strip()
            if line.startswith('data: '):
                data=json.loads(line[6:])
                if 'frame' in data:
                    at=datetime.datetime.fromisoformat(data['frame']['at'].replace('Z','+00:00')).timestamp()
                    latencies.append(max(0,time.time()-at))

async def main():
    f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
    pool=[]
    semaphore=asyncio.Semaphore(32)
    async def connect(i):
        async with semaphore:
            reader,writer=await asyncio.wait_for(asyncio.open_connection('127.0.0.1',f.HTTP),20)
            writer.write(b'CONNECT origin:9001 HTTP/1.1\r\nHost: origin:9001\r\n\r\n')
            await writer.drain()
            reply=await asyncio.wait_for(reader.readuntil(b'\r\n\r\n'),25)
            assert b' 200 ' in reply,reply[:100]
            writer.write(b'identity:'+str(i).encode()+b'\n');await writer.drain()
            assert await asyncio.wait_for(reader.readline(),20)==b'identity:'+str(i).encode()+b'\n'
            pool.append((reader,writer))
    started=time.monotonic()
    try:
        await asyncio.gather(*(connect(i) for i in range(COUNT)))
        print(json.dumps({'connected':len(pool),'setup_seconds':time.monotonic()-started}),flush=True)
        # Sync frames are observed once per second; start the full hold only
        # after the control plane has actually observed every established tunnel.
        deadline=time.monotonic()+15
        while time.monotonic()<deadline:
            initial=await asyncio.to_thread(f.request,'topology/snapshot')
            active=sum(r['active'] for w in initial['workers'] for r in w['routes'])
            if COUNT<=active<=COUNT+4:break
            await asyncio.sleep(.25)
        else:raise AssertionError(('initial exact active count not observed',active))
        print(json.dumps({'confirmed_active':active,'hold_seconds_requested':SECONDS}),flush=True)
        thread=threading.Thread(target=live,daemon=True);thread.start()
        samples=[];start=time.monotonic();iteration=0
        while time.monotonic()-start<SECONDS:
            # Keep every tunnel exercised each minute; no large payload buffers.
            subset=pool[iteration*100%COUNT:iteration*100%COUNT+100]
            async def ping(pair):
                reader,writer=pair;writer.write(b'x'*128);await writer.drain();assert await asyncio.wait_for(reader.readexactly(128),15)==b'x'*128
            await asyncio.gather(*(ping(pair) for pair in subset))
            # Separate 5000-budget policy: keep selection events flowing while
            # all 5000 primary-policy slots remain occupied.
            await asyncio.to_thread(__import__('acceptance').proxy_http,f.HTTP_AUTH,'/healthz')
            if iteration%10==0:
                snapshot=await asyncio.to_thread(f.request,'topology/snapshot')
                total=sum(r['active'] for w in snapshot['workers'] for r in w['routes'])
                assert COUNT<=total<=COUNT+4,('active tunnels changed',total)
                with urllib.request.urlopen('http://127.0.0.1:39090/metrics',timeout=10) as response:metrics=response.read().decode()
                values={line.split()[0]:float(line.split()[1]) for line in metrics.splitlines() if line and not line.startswith('#') and '{' not in line}
                sample={'elapsed':time.monotonic()-start,'active':total,'heap':values['go_memstats_heap_alloc_bytes'],'rss':values['process_resident_memory_bytes'],'buffer_fill':values.get('relayweave_topology_buffer_fill',0),'snapshot_bytes':len(json.dumps(snapshot))}
                samples.append(sample);print(json.dumps(sample),flush=True)
            iteration+=1;await asyncio.sleep(1)
        stop.set()
        warm=[s['heap'] for s in samples if s['elapsed']>=min(300,SECONDS/3)]
        growth=(statistics.median(warm[-6:])-statistics.median(warm[:6])) if len(warm)>=12 else None
        p95=sorted(latencies)[min(len(latencies)-1,int(len(latencies)*.95))] if latencies else None
        report={'actual_connections':COUNT,'actual_seconds':time.monotonic()-start,'samples':samples,'sse_delay_p95_seconds':p95,'sse_samples':len(latencies),'heap_median_growth_bytes':growth,'passed':COUNT==5000 and time.monotonic()-start>=1800 and p95 is not None and p95<=2 and growth is not None and growth<=64*1024*1024,'limits':'SSE transport delay is not browser paint latency; heap growth is not an isolated allocation attribution'}
        Path('/reports/topology-load.json').write_text(json.dumps(report,indent=2));print(json.dumps({k:v for k,v in report.items() if k!='samples'}),flush=True)
    finally:
        stop.set()
        for _,writer in pool:writer.close()
        await asyncio.gather(*(writer.wait_closed() for _,writer in pool),return_exceptions=True)

asyncio.run(main())
