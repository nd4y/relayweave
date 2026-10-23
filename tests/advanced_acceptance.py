"""Revocation, session draining, backup restore and support bundle in the full stack."""
import gzip
import json
import os
import socket
import time
import urllib.request
from pathlib import Path
import full_acceptance as f
import acceptance as protocol

checks=[]
def check(name,fn):
    started=time.monotonic();result=fn();checks.append({'name':name,'passed':True,'seconds':round(time.monotonic()-started,3)});print('PASS',name,flush=True);return result
def assert_(value):assert value
def until(fn,seconds=10):
    start=time.monotonic()
    while time.monotonic()-start<seconds:
        try:
            if fn():return time.monotonic()-start
        except (OSError,AssertionError):pass
        time.sleep(.1)
    raise AssertionError('condition not reached before deadline')
def raw(path,body=b'',content_type='application/json',expected=200):
    req=urllib.request.Request(f.BASE+path,data=body,method='POST',headers={'X-CSRF-Token':f.csrf,'Content-Type':content_type})
    with f.client.open(req,timeout=90) as response:
        assert response.status==expected
        return response.read()
def multipart(data,password):
    boundary='relayweave-test-boundary'
    body=(f'--{boundary}\r\nContent-Disposition: form-data; name="password"\r\n\r\n{password}\r\n--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="test.rwb"\r\nContent-Type: application/octet-stream\r\n\r\n').encode()+data+f'\r\n--{boundary}--\r\n'.encode()
    return body,'multipart/form-data; boundary='+boundary
def tcp_works(port,auth=None):
    try:
        c,_=protocol.socks_connect(port,target='origin',target_port=9001,auth=auth)
        with c:c.sendall(b'live');return c.recv(4)==b'live'
    except Exception:return False

def main():
    f.csrf=f.request('auth/login','POST',{'password':f.PASSWORD})['csrf']
    ids=json.loads(Path('/reports/integration-ids.json').read_text())
    profile=f.request('profiles/'+ids['profile_primary'])
    if not profile['data']['enabled'] or not tcp_works(f.SOCKS):
        f.patch(profile,{'enabled':True});until(lambda:tcp_works(f.SOCKS),15);profile=f.request('profiles/'+profile['id'])
    check('profile delete rejected; disable preserves source ownership',lambda:f.request('profiles/'+profile['id'],'DELETE',version=profile['version'],expected=405))
    existing,_=protocol.socks_connect(f.SOCKS,target='origin',target_port=9001)
    try:
        started=time.monotonic();f.patch(profile,{'enabled':False})
        delay=until(lambda:not tcp_works(f.SOCKS),5)
        check('online profile revocation within 5 seconds',lambda:assert_(time.monotonic()-started<=5))
        check('existing TCP drains after profile revocation',lambda:echo(existing,b'established-session'))
        check('other Xray version unaffected by revocation',lambda:assert_(tcp_works(f.SOCKS_AUTH,('lab-user','lab-pass'))))
        profile=f.request('profiles/'+profile['id']);f.patch(profile,{'enabled':True});until(lambda:tcp_works(f.SOCKS),15)
        check('profile re-enable restores new traffic',lambda:assert_(tcp_works(f.SOCKS)))
        source=f.request('sources/'+ids['source_primary']);before=profile['id']
        f.operation('sources/'+source['id']+'/refresh')
        check('304 refresh retains stable profile ID',lambda:assert_(f.request('profiles/'+before)['data']['present']))
        # Structural listener changes are a draft until Apply.
        listener=next(x for x in f.request('listeners')['items'] if x['data']['listen'].endswith(':1081'))
        f.patch(listener,{'password':'rotated-lab-password'})
        check('listener draft does not change active password',lambda:assert_(tcp_works(f.SOCKS_AUTH,('lab-user','lab-pass'))))
        f.operation('config/apply',timeout=210)
        check('listener password changed after Apply',lambda:assert_(tcp_works(f.SOCKS_AUTH,('lab-user','rotated-lab-password')) and not tcp_works(f.SOCKS_AUTH,('lab-user','lab-pass'))))
        listener=f.request('listeners/'+listener['id']);f.patch(listener,{'password':'lab-pass'});f.operation('config/apply',timeout=210)
        check('existing TCP survives repeated revisions',lambda:echo(existing,b'after-password-revisions'))
        password='fixture-backup-password-2026'
        backup=check('encrypted portable backup',lambda:raw('backup',json.dumps({'password':password}).encode()))
        check('backup bytes contain no plaintext test credentials',lambda:assert_(b'lab-pass' not in backup and b'00000000-0000-0000-0000-000000000001' not in backup))
        body,kind=multipart(backup,password)
        preview=json.loads(raw('backup/preview',body,kind));check('restore preview validates counts and apply boundary',lambda:assert_(preview['valid'] and preview['apply_required']))
        result=json.loads(raw('backup/restore',body,kind));check('restore preserves deployment identity',lambda:assert_(result['restored'] and result['deployment_identity_preserved']))
        check('restored draft is held for explicit Apply',lambda:assert_(f.request('overview')['restore_pending']))
        f.operation('sources/'+source['id']+'/refresh');time.sleep(2)
        check('source refresh cannot release restored draft',lambda:assert_(f.request('overview')['restore_pending']))
        check('old traffic survives restore before Apply',lambda:echo(existing,b'restore-draft'))
        f.operation('config/apply',timeout=210)
        check('successful Apply clears restored draft hold',lambda:assert_(not f.request('overview')['restore_pending']))
        check('restored configuration carries TCP/UDP traffic',lambda:(assert_(tcp_works(f.SOCKS)),f.udp_echo(f.SOCKS_AUTH,('lab-user','lab-pass'))))
        manifest=f.request('support');check('support manifest explicitly identifies excluded secrets',lambda:assert_(len(manifest['excluded'])>=6))
        support=gzip.decompress(raw('support',b'{}'))
        check('support archive excludes known credentials and raw source URL',lambda:assert_(all(value not in support for value in [b'lab-pass',b'rotated-lab-password',b'00000000-0000-0000-0000-000000000001',b'http://subscription:9004/subscription'])))
        check('support archive schema and worker status',lambda:assert_(json.loads(support)['schema']==1 and len(json.loads(support)['workers'])>=1))
    finally:existing.close()
    report={'passed':True,'checks':checks,'profile_revocation_seconds':round(delay,3)}
    Path('/reports/advanced-acceptance.json').write_text(json.dumps(report,indent=2));print(json.dumps({'passed':True,'checks':len(checks)}),flush=True)

def echo(c,payload):
    c.settimeout(5);c.sendall(payload);assert protocol.exact(c,len(payload))==payload

if __name__=='__main__':
    try:main()
    except BaseException:
        Path('/reports/advanced-acceptance-failed.json').write_text(json.dumps({'passed':False,'checks':checks},indent=2));raise
