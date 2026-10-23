"""Synthetic external-netns bridge/host comparison client. No production data."""
import concurrent.futures as futures
import json,os,socket,ssl,time,statistics
import acceptance as p
p.HOST=os.environ['BENCH_GATEWAY']
auth=('lab-user','lab-pass')
held=[]
def tunnel(_):
    s,_=p.socks_connect(31380,'origin',9001,auth)
    s.sendall(b'x');assert p.exact(s,1)==b'x';held.append(s)
def bulk(seconds):
    end=time.monotonic()+seconds
    def transfer(_):
        n=0;lat=[]
        while time.monotonic()<end:
            t=time.monotonic();code,_,body=p.proxy_http(38380,'/bytes?n=8388608',auth=auth)
            assert code==200 and len(body)==8388608 and body[:1]==b'x'
            n+=len(body);lat.append(time.monotonic()-t)
        return n,lat
    t=time.monotonic()
    with futures.ThreadPoolExecutor(4) as pool:rows=list(pool.map(transfer,range(4)))
    elapsed=time.monotonic()-t;lat=sorted(v for _,xs in rows for v in xs)
    return {'bytes':sum(n for n,_ in rows),'seconds':elapsed,'mbps':sum(n for n,_ in rows)*8/elapsed/1e6,'transfer_p95_seconds':lat[int((len(lat)-1)*.95)],'transfers':len(lat),'errors':0}
try:
    assert p.proxy_http(38380)[0]==407
    with socket.create_connection((p.HOST,38380),timeout=10) as s:
        import base64
        token=base64.b64encode(b'lab-user:lab-pass').decode()
        s.sendall(f'CONNECT origin:9443 HTTP/1.1\r\nHost: origin:9443\r\nProxy-Authorization: Basic {token}\r\n\r\n'.encode())
        h=b''
        while not h.endswith(b'\r\n\r\n'):h+=p.exact(s,1)
        assert h.startswith(b'HTTP/1.1 200 ')
        with ssl.create_default_context(cafile='/lab/ca.pem').wrap_socket(s,server_hostname='origin') as tls:
            tls.sendall(b'GET /healthz HTTP/1.1\r\nHost: origin\r\nConnection: close\r\n\r\n');r=p.response(tls);assert r.status==200 and r.read()==b'ok\n'
    ctrl,bound=p.socks_connect(31380,'0.0.0.0',0,auth,command=3)
    assert bound[0]==p.HOST and 38300<=bound[1]<=38331,bound
    with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as udp:
        udp.settimeout(8)
        for size in [1,1200,4096]:
            payload=os.urandom(size);udp.sendto(b'\0\0\0'+p.address('origin',9002)+payload,bound);assert p.datagram_payload(udp.recvfrom(65535)[0])==payload
    ctrl.close()
    with futures.ThreadPoolExecutor(16) as pool:list(pool.map(tunnel,range(512)))
    bulk(10)
    print(json.dumps({'stage':'ready_measure','held':len(held),'udp':True,'http_connect_tls':True,'auth':True}),flush=True)
    assert input()=='go'
    result=bulk(60)
    print(json.dumps({'stage':'measured',**result}),flush=True)
    assert input()=='finish'
    for s in held:s.sendall(b'z');assert p.exact(s,1)==b'z'
    print(json.dumps({'stage':'verified','idle_tunnels':len(held)}),flush=True)
finally:
    for s in held:s.close()
