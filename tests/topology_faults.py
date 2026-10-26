"""Foreground worker/CP recovery handshake; controller only touches owned IDs."""
import json
import socket
import sys
import time
from pathlib import Path
import full_acceptance as f
f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
initial=f.request('topology/snapshot');epoch=initial['workers'][0]['epoch']
with socket.create_connection(('127.0.0.1',f.HTTP),timeout=10) as sock:
    sock.sendall(b'CONNECT origin:9001 HTTP/1.1\r\nHost: origin:9001\r\n\r\n')
    assert b' 200 ' in sock.recv(4096)
    sock.sendall(b'before-pause');assert sock.recv(4096)==b'before-pause'
    print(json.dumps({'stage':'ready'}),flush=True)
    assert sys.stdin.readline().strip()=='paused'
    started=time.monotonic();count=0
    while time.monotonic()-started<12:
        payload=('outage-echo-%d'%count).encode();sock.sendall(payload);assert sock.recv(4096)==payload;count+=1;time.sleep(.25)
    print(json.dumps({'stage':'outage_verified','echoes':count}),flush=True)
    assert sys.stdin.readline().strip()=='restored'
    sock.sendall(b'after-pause');assert sock.recv(4096)==b'after-pause'
deadline=time.monotonic()+60
while time.monotonic()<deadline:
    current=f.request('topology/snapshot')
    if current['workers'] and current['workers'][0]['epoch']==epoch and current['cursor']!=initial['cursor']:break
    time.sleep(.5)
else:raise AssertionError('worker did not resume observation')
print(json.dumps({'stage':'ready_worker_restart'}),flush=True)
assert sys.stdin.readline().strip()=='worker_restarted'
deadline=time.monotonic()+90
while time.monotonic()<deadline:
    current=f.request('topology/snapshot')
    if current['workers'] and current['workers'][0]['epoch']!=epoch:break
    time.sleep(.5)
else:raise AssertionError('worker epoch did not change')
print(json.dumps({'stage':'ready_cp_restart'}),flush=True)
assert sys.stdin.readline().strip()=='cp_restarted'
deadline=time.monotonic()+90
while time.monotonic()<deadline:
    try:
        after=f.request('topology/snapshot')
        if after['workers'] and after['cursor'].split(':')[0]!=initial['cursor'].split(':')[0]:break
    except Exception:pass
    time.sleep(.5)
else:raise AssertionError('control epoch did not change')
report={'passed':True,'outage_seconds':12,'existing_tunnel_echoes':count,'worker_epoch_changed':True,'control_epoch_changed':True,'details':'existing TCP remained usable while own control plane was paused; independent worker/control restarts produced new epochs'}
Path('/reports/topology-faults.json').write_text(json.dumps(report,indent=2));print(json.dumps({'stage':'finished',**report}),flush=True)
