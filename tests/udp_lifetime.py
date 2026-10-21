"""70-second one-way UDP must not expire at the 60-second idle threshold."""
import concurrent.futures
import json
import os
import socket
import time
from pathlib import Path
import acceptance as p

def scenario(port,auth,direction):
    control,bound=p.socks_connect(port,target='0.0.0.0',target_port=0,command=3,auth=auth)
    with control,socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as udp:
        udp.settimeout(5)
        prefix=b'\0\0\0'+p.address('origin',9004 if direction=='upload' else 9005)
        start=time.monotonic();packets=0
        if direction=='download':udp.sendto(prefix+b'start',bound)
        while time.monotonic()-start<70:
            if direction=='upload':udp.sendto(prefix+b'one-way-upload',bound);time.sleep(1)
            else:
                data,_=udp.recvfrom(8192);assert data.endswith(b'one-way-download')
            packets+=1
        payload=b'after-seventy-seconds-'+direction.encode()
        udp.sendto(b'\0\0\0'+p.address('origin',9002)+payload,bound)
        deadline=time.monotonic()+5
        while time.monotonic()<deadline:
            data,_=udp.recvfrom(8192)
            if data.endswith(payload):
                return {'direction':direction,'port':port,'authenticated':auth is not None,'seconds':round(time.monotonic()-start,3),'one_way_packets':packets,'passed':True}
        raise AssertionError('association expired despite one-way traffic')

def main():
    primary=int(os.environ.get('SOCKS_PORT',41080));secondary=int(os.environ.get('SOCKS_AUTH_PORT',41081))
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        jobs=[pool.submit(scenario,port,auth,direction) for port,auth in [(primary,None),(secondary,('lab-user','lab-pass'))] for direction in ['upload','download']]
        rows=[job.result() for job in jobs]
    report={'passed':all(x['passed'] for x in rows),'checks':rows};Path('/reports/udp-lifetime.json').write_text(json.dumps(report,indent=2));print(json.dumps(report),flush=True)
if __name__=='__main__':main()
