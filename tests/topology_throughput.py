"""Identical bounded transfer workload for each enabled/disabled paired run."""
import concurrent.futures
import json
import os
import time
import threading
import urllib.request
import acceptance as p
from pathlib import Path
port=int(os.getenv('HTTP_PORT','38080'))
seconds=12
total=0
held=[]
held_lock=threading.Lock()
def hold(_):
    sock,_=p.socks_connect(31080,target='origin',target_port=9001)
    with held_lock:held.append(sock)
    sock.sendall(b'x');assert p.exact(sock,1)==b'x'
def verify(sock):sock.sendall(b'z');assert p.exact(sock,1)==b'z'
def transfer(_):
    count=0
    while time.monotonic()-start<seconds:
        code,_,body=p.proxy_http(port,'/bytes?n=8388608')
        assert code==200 and len(body)==8388608 and body[:1]==b'x'
        count+=len(body)
    return count
try:
    with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:list(pool.map(hold,range(4990)))
    assert len(held)==4990
    with urllib.request.urlopen('http://127.0.0.1:39090/metrics',timeout=10) as response:metrics=response.read().decode()
    memory={line.split()[0]:float(line.split()[1]) for line in metrics.splitlines() if line and not line.startswith('#') and '{' not in line}
    start=time.monotonic()
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:total=sum(pool.map(transfer,range(4)))
    elapsed=time.monotonic()-start
    with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:list(pool.map(verify,held))
finally:
    for sock in held:sock.close()
report={'bytes':total,'seconds':elapsed,'mbps':total*8/elapsed/1e6,'pair':int(os.environ['TOPOLOGY_PAIR']),'enabled':os.environ['TOPOLOGY_ENABLED']=='true','verified_idle_tunnels':len(held),'bulk_concurrency':4,'nominal_active':len(held)+4,'worker_heap_bytes':memory['go_memstats_heap_alloc_bytes'],'worker_rss_bytes':memory['process_resident_memory_bytes']}
name=f"topology-throughput-{report['pair']}-{'on' if report['enabled'] else 'off'}.json"
Path('/reports/'+name).write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)
