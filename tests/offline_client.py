"""Continuous old/new traffic while the orchestrator stops only our API and DB."""
import json
import socket
import time
import urllib.error
import urllib.request
from pathlib import Path
import full_acceptance as f
import acceptance as p

def main():
    streams=[];rows=[];restart_seen=False;last_report=0
    def open_streams():
        return [p.socks_connect(port,target='origin',target_port=9001,auth=auth)[0] for port,auth in [(f.SOCKS,None),(f.SOCKS_AUTH,('lab-user','lab-pass'))]]
    streams=open_streams();Path('/reports/outage-client-ready.json').write_text('{}')
    start=time.monotonic();outage_start=None;was_down=False
    try:
        while True:
            assert time.monotonic()-start<2200,'Outage controller exceeded its deadline'
            phase=json.loads(Path('/reports/outage-phase.json').read_text())['phase']
            assert phase!='aborted','Outage controller aborted'
            if phase=='restart' and not restart_seen:
                for stream in streams:stream.close()
                streams=[];restart_seen=True
            if phase=='restarted' and not streams:
                deadline=time.monotonic()+30
                while True:
                    try:streams=open_streams();break
                    except (OSError,AssertionError,EOFError):
                        if time.monotonic()>deadline:raise
                        time.sleep(.5)
            if phase!='restart':
                for stream in streams:
                    stream.settimeout(5);stream.sendall(b'offline-lease');assert p.exact(stream,13)==b'offline-lease'
                assert p.proxy_http(f.HTTP)[0]==200
                assert p.socks_http(f.SOCKS_AUTH,auth=('lab-user','lab-pass'))[0]==200
                f.udp_echo(f.SOCKS)
                f.udp_echo(f.SOCKS_AUTH,('lab-user','lab-pass'))
            try:
                with urllib.request.urlopen('http://127.0.0.1:19091/healthz',timeout=2) as response:api_up=response.status==200
            except (OSError,urllib.error.URLError):api_up=False
            if not api_up:
                was_down=True
                if outage_start is None:outage_start=time.monotonic()
            if time.monotonic()-last_report>30:
                row={'elapsed_seconds':round(time.monotonic()-start,1),'phase':phase,'api_up':api_up,'streams':len(streams),'traffic_passed':phase!='restart'};rows.append(row);print(json.dumps(row),flush=True);last_report=time.monotonic()
            if phase=='complete':
                assert api_up and was_down and restart_seen
                f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
                deadline=time.monotonic()+10
                while not all(w['online'] for w in f.request('workers')['items']):
                    if time.monotonic()>deadline:raise AssertionError('worker did not reconnect after API recovery')
                    time.sleep(.5)
                break
            time.sleep(2)
    finally:
        for stream in streams:stream.close()
    report={'passed':True,'worker_restarted_offline':restart_seen,'checks':rows,'elapsed_seconds':round(time.monotonic()-start,1),'scope':'Existing TCP until planned worker restart; new HTTP/SOCKS5/UDP traffic throughout API+DB outage; planned restart resets old TCP.'}
    Path('/reports/offline-recovery.json').write_text(json.dumps(report,indent=2));print('PASS 30-minute API/database outage and offline worker restart',flush=True)
if __name__=='__main__':main()
