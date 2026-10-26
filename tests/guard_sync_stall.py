"""Own PostgreSQL status-row contention must not delay live admission changes."""
import json
import hashlib
import os
from pathlib import Path
import selectors
import re
import subprocess
import sys
import time

root = Path('/home/av/build/relayweave')
source = Path.cwd().resolve()
env = dict(line.split('=', 1) for line in (root/'integration.env').read_text().splitlines() if '=' in line)
assert source.parent == root/'source' and env['SOURCE_SNAPSHOT'] == source.name
for key in ('ADMIN_PASSWORD', 'HTTP_PORT', 'HTTP_AUTH_PORT', 'SOCKS_PORT', 'SOCKS_AUTH_PORT'):
    os.environ[key] = env[key]
sys.path.insert(0, str(source/'tests'))
import full_acceptance as f
import acceptance as p
from advanced_acceptance import tcp_works, echo, until

run_id = os.environ.get('GUARD_SYNC_RUN_ID', '')
assert not run_id or re.fullmatch(r'[a-z0-9-]{1,32}', run_id), 'Invalid diagnostic run id'
report_path = root/'reports'/(source.name+'-guard-sync-stall'+('-'+run_id if run_id else '')+'.json')
assert not report_path.exists(), 'Preserve prior contention evidence'
ids = json.loads((source/'reports/integration-ids.json').read_text())
f.csrf = f.request('auth/login', 'POST', {'password': f.PASSWORD})['csrf']
record = {'source': source.name, 'case': 'revocation_during_status_persistence_contention', 'passed': False, 'limit_seconds': 5, 'script_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest()}
process = None
stream = None
changed = False
command = ['docker', 'exec', '-i', '--user', 'postgres', 'relayweave-database-1',
           'psql', '-X', '-q', '-A', '-t', '-U', 'relayweave', '-d', 'relayweave', '-v', 'ON_ERROR_STOP=1']
try:
    database = json.loads(subprocess.check_output(['docker', 'inspect', 'relayweave-database-1'], timeout=10))[0]
    labels = database['Config']['Labels']
    assert labels['com.docker.compose.project'] == 'relayweave' and labels['io.relayweave.test-stack'] == 'true'
    mount = next(x for x in database['Mounts'] if x['Destination'] == '/var/lib/postgresql')
    assert Path(mount['Source']).resolve() == Path(env['DATA_ROOT']).resolve()/'postgres'
    workers = f.request('workers')['items']
    assert len(workers) == 2 and all(x['online'] for x in workers), 'Healthy worker control connections required before contention'
    item = f.request('sources/'+ids['source_primary'])
    assert item['data']['enabled'] and tcp_works(f.SOCKS)
    stream, _ = p.socks_connect(f.SOCKS, target_port=9001)
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
    # Only a status table in this verified synthetic database is locked. No rows
    # change; the same session automatically commits and exits after30seconds.
    # This outlasts bounded Docker output/detection latency plus the5s gate.
    sql = ("SET statement_timeout='35s';\nBEGIN;\nLOCK TABLE worker_status IN ACCESS EXCLUSIVE MODE;\n"
           "\\echo RELAYWEAVE_STATUS_LOCK_HELD\nSELECT pg_sleep(30);\nCOMMIT;\n\\q\n")
    process.stdin.write(sql.encode())
    process.stdin.flush()
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    captured = b''
    deadline = time.monotonic()+15
    while b'RELAYWEAVE_STATUS_LOCK_HELD\n' not in captured:
        assert time.monotonic() < deadline and process.poll() is None, 'Status lock did not start'
        for key, _ in selector.select(.2):
            captured += os.read(key.fd, 4096)
    selector.close()
    query = "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'INSERT INTO worker_status%';"
    blocked = 0
    deadline = time.monotonic()+5
    while blocked < len(workers) and time.monotonic() < deadline:
        raw = subprocess.check_output(command+['-c', query], timeout=8).decode().strip()
        blocked = int(raw)
        if blocked < len(workers):
            time.sleep(.1)
    assert blocked >= len(workers), 'Both normal worker status writers must be blocked before revocation'
    record['blocked_status_writers_observed'] = blocked
    started = time.monotonic()
    f.patch(item, {'enabled': False})
    changed = True
    record['source_disable_request_seconds'] = time.monotonic()-started
    denied = None
    while time.monotonic()-started < 5:
        if not tcp_works(f.SOCKS):
            denied = time.monotonic()-started
            break
        time.sleep(.1)
    record['admission_blocked_seconds'] = denied
    echo(stream, b'status-write-contention-existing-session')
    assert tcp_works(f.SOCKS_AUTH, ('lab-user', 'lab-pass'))
    record.update(existing_tcp_drains=True, other_source_unaffected=True)
    assert denied is not None and denied <= 5, 'Admission change waited for status persistence'
    record['passed'] = True
except Exception as error:
    record['error'] = repr(error)
finally:
    cleanup_errors = []
    try:
        if process is not None:
            collected = time.monotonic()
            # The SQL sleep/statement stay bounded30/35s. Docker may acknowledge
            # the exited exec later; keep that collection budget separate.
            try:
                process.communicate(timeout=90)
            except subprocess.TimeoutExpired:
                record['status_client_ack_timeout'] = True
                # Only this owned host-side Docker client is reaped. Its queued
                # SQL COMMIT and statement timeout remain on the server.
                process.kill()
                process.communicate(timeout=10)
                raise
            finally:
                record['status_client_collect_seconds'] = time.monotonic()-collected
            assert process.returncode == 0, 'Own status-lock session failed'
    except Exception as error:
        cleanup_errors.append(repr(error))
    try:
        if changed:
            f.patch(f.request('sources/'+item['id']), {'enabled': True})
            f.operation('config/apply', timeout=210)
            until(lambda: tcp_works(f.SOCKS), 30)
            assert p.proxy_http(f.HTTP)[0] == 200
            f.udp_echo(f.SOCKS)
        record['cleanup_restored_traffic'] = True
    except Exception as error:
        cleanup_errors.append(repr(error))
    finally:
        if cleanup_errors:
            record.update(cleanup_errors=cleanup_errors, passed=False)
        if stream is not None:
            stream.close()
        record['scope'] = 'Own labelled synthetic PostgreSQL table lock; actual blocked status writes and API source change, no host or other database changes.'
        report_path.write_text(json.dumps(record, indent=2))
        print(json.dumps(record), flush=True)
if not record['passed']:
    raise SystemExit(1)
