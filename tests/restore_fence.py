"""The restored draft and independent admission publication are atomic."""
import concurrent.futures
import json
import os
from pathlib import Path
import selectors
import subprocess
import sys
import time

root = Path('/home/av/build/relayweave')
source = Path.cwd().resolve()
env = dict(line.split('=', 1) for line in (root / 'integration.env').read_text().splitlines() if '=' in line)
assert source.parent == root / 'source' and env['SOURCE_SNAPSHOT'] == source.name
for key in ('ADMIN_PASSWORD', 'HTTP_PORT', 'HTTP_AUTH_PORT', 'SOCKS_PORT', 'SOCKS_AUTH_PORT'):
    os.environ[key] = env[key]
sys.path.insert(0, str(source / 'tests'))
import full_acceptance as f
import acceptance as p
from advanced_acceptance import raw, multipart, tcp_works, echo

report_path = root / 'reports' / (source.name + '-restore-fence.json')
assert not report_path.exists(), 'Preserve prior restore-fence evidence'
database = json.loads(subprocess.check_output(['docker', 'inspect', 'relayweave-database-1']))[0]
labels = database['Config']['Labels']
assert labels['com.docker.compose.project'] == 'relayweave' and labels['io.relayweave.test-stack'] == 'true'
mount = next(x for x in database['Mounts'] if x['Destination'] == '/var/lib/postgresql')
assert Path(mount['Source']).resolve() == Path(env['DATA_ROOT']).resolve() / 'postgres'
ids = json.loads((source / 'reports/integration-ids.json').read_text())
f.csrf = f.request('auth/login', 'POST', {'password': f.PASSWORD})['csrf']
password = 'synthetic-restore-fence-backup-2026'
body, kind = multipart(raw('backup', json.dumps({'password': password}).encode()), password)
item = f.request('sources/' + ids['source_primary'])
stream, _ = p.socks_connect(f.SOCKS, target_port=9001)
command = ['docker', 'exec', '-i', '--user', 'postgres', 'relayweave-database-1',
           'psql', '-X', '-q', '-A', '-t', '-U', 'relayweave', '-d', 'relayweave', '-v', 'ON_ERROR_STOP=1']
record = {'source': source.name, 'case': 'restore_serializes_with_live_fence_publication', 'passed': False}
process = None
pool = concurrent.futures.ThreadPoolExecutor(max_workers=1)
future = None
try:
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
    process.stdin.write(("SET statement_timeout='20s';\nSELECT pg_advisory_lock(723501993);\n"
                         "\\echo RELAYWEAVE_FENCE_HELD\nSELECT pg_sleep(8);\nSELECT pg_advisory_unlock(723501993);\n\\q\n").encode())
    process.stdin.flush()
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    captured = b''
    deadline = time.monotonic() + 25
    while b'RELAYWEAVE_FENCE_HELD\n' not in captured:
        assert time.monotonic() < deadline and process.poll() is None, 'Fence lock fixture did not start'
        for key, _ in selector.select(.2):
            captured += os.read(key.fd, 4096)
    selector.close()
    started = time.monotonic()
    future = pool.submit(raw, 'backup/restore', body, kind)
    deadline = started + 5
    observed = False
    while time.monotonic() < deadline and not future.done():
        query = "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND classid=0 AND objid=723501993 AND NOT granted;"
        pending = subprocess.check_output(command + ['-c', query], timeout=5).decode().strip()
        if pending == '1':
            observed = True
            break
        time.sleep(.1)
    assert observed, 'Import bypassed the in-progress fence publication'
    assert not future.done(), 'Import completed while fence publication was locked'
    assert f.request('sources/' + item['id'])['version'] == item['version'], 'Import changed catalogue before acquiring its fence lock'
    assert not f.request('overview')['restore_pending']
    echo(stream, b'restore-waits-for-fence')
    assert tcp_works(f.SOCKS_AUTH, ('lab-user', 'lab-pass'))
    record.update(import_wait_observed=True, catalogue_unchanged_while_waiting=True, existing_tcp_drains=True)
    process.communicate(timeout=30)
    assert process.returncode == 0
    restored = json.loads(future.result(timeout=35))
    assert restored['restored'] and f.request('overview')['restore_pending']
    record['restore_seconds'] = time.monotonic() - started
    f.operation('sources/' + item['id'] + '/refresh')
    assert f.request('overview')['restore_pending'], 'Source refresh released restored draft'
    echo(stream, b'restored-draft-still-held')
    record['explicit_apply_hold_preserved'] = True
    record['passed'] = True
except Exception as error:
    record.update(error=repr(error), passed=False)
finally:
    try:
        if process is not None:
            process.communicate(timeout=35)
        if future is not None:
            future.result(timeout=35)
        f.operation('config/apply', timeout=210)
        assert not f.request('overview')['restore_pending']
        assert tcp_works(f.SOCKS) and tcp_works(f.SOCKS_AUTH, ('lab-user', 'lab-pass'))
        f.udp_echo(f.SOCKS)
        record['cleanup_restored_traffic'] = True
    except Exception as error:
        record.update(cleanup_error=repr(error), passed=False)
    pool.shutdown(wait=True)
    stream.close()
    report_path.write_text(json.dumps(record, indent=2))
    print(json.dumps(record), flush=True)
if not record['passed']:
    raise SystemExit(1)
