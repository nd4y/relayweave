"""Alternating connect/first-echo samples, same auth/core/upstream on both paths."""
import base64
import json
import math
import socket
import statistics
import time
from pathlib import Path
import acceptance as p

def sample(protocol,port,auth):
    start=time.perf_counter()
    if protocol=='socks5':c,_=p.socks_connect(port,target='origin',target_port=9001,auth=auth)
    else:
        c=socket.create_connection(('127.0.0.1',port),timeout=8)
        credential='' if not auth else 'Proxy-Authorization: Basic '+base64.b64encode(':'.join(auth).encode()).decode()+'\r\n'
        c.sendall(('CONNECT origin:9001 HTTP/1.1\r\nHost: origin:9001\r\n'+credential+'\r\n').encode())
        data=b''
        while not data.endswith(b'\r\n\r\n'):
            data+=p.exact(c,1);assert len(data)<32768
        assert data.startswith(b'HTTP/1.1 200'),data[:80]
    connected=time.perf_counter()-start
    with c:
        c.sendall(b'latency-fixture');assert p.exact(c,15)==b'latency-fixture'
    return [connected*1000,(time.perf_counter()-start)*1000]
def quantile(values,q):return sorted(values)[math.ceil(len(values)*q)-1]
def main():
    groups=[]
    for offset in [0,1]:
        auth=None if offset==0 else ('lab-user','lab-pass')
        for protocol in ['socks5','http']:
            ports={'baseline':29101+offset if protocol=='socks5' else 29201+offset,'application':41080+offset if protocol=='socks5' else 48080+offset}
            for port in ports.values():
                for _ in range(10):sample(protocol,port,auth)
            rows={'baseline':[],'application':[]}
            for i in range(300):
                for variant in (['baseline','application'] if i%2==0 else ['application','baseline']):rows[variant].append(sample(protocol,ports[variant],auth))
            stats={variant:{'connect_p50_ms':statistics.median(x[0] for x in values),'connect_p95_ms':quantile([x[0] for x in values],.95),'first_echo_p95_ms':quantile([x[1] for x in values],.95)} for variant,values in rows.items()}
            extra=stats['application']['connect_p95_ms']-stats['baseline']['connect_p95_ms']
            group={'version':'26.10.17' if offset==0 else '26.9.30','protocol':protocol,'samples_per_variant':300,'stats':stats,'extra_connect_p95_ms':extra,'passed':extra<=5,'raw_ms':rows}
            groups.append(group);print(json.dumps({k:v for k,v in group.items() if k!='raw_ms'}),flush=True)
    report={'passed':all(x['passed'] for x in groups),'groups':groups,'scope':'Local published connect handshake and independent end-to-end first echo; synthetic VLESS/TLS. No WAN latency claim.'}
    Path('/reports/connect-latency.json').write_text(json.dumps(report,indent=2));assert report['passed']
if __name__=='__main__':main()
