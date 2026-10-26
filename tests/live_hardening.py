"""Own synthetic deployment: rollout contention and existing-path canary checks."""
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
from advanced_acceptance import tcp_works, echo, until

report_path = root / 'reports' / (source.name + '-live-hardening.json')
assert not report_path.exists(), 'Preserve prior hardening evidence'
ids = json.loads((source / 'reports/integration-ids.json').read_text())
f.csrf = f.request('auth/login', 'POST', {'password': f.PASSWORD})['csrf']
checks = []


def check_contention():
    record = {'case': 'live_source_revocation_during_configuration_lock', 'limit_seconds': 5, 'passed': False}
    workers = f.request('workers')['items']
    record['workers_online_before_fixture'] = bool(workers) and all(x['online'] for x in workers)
    if not record['workers_online_before_fixture']:
        record['error'] = 'worker_control_unavailable_before_fixture'
        return record
    database = json.loads(subprocess.check_output(['docker', 'inspect', 'relayweave-database-1']))[0]
    labels = database['Config']['Labels']
    assert labels['com.docker.compose.project'] == 'relayweave' and labels['io.relayweave.test-stack'] == 'true'
    mount = next(x for x in database['Mounts'] if x['Destination'] == '/var/lib/postgresql')
    assert Path(mount['Source']).resolve() == Path(env['DATA_ROOT']).resolve() / 'postgres'
    assert tcp_works(f.SOCKS) and tcp_works(f.SOCKS_AUTH, ('lab-user', 'lab-pass'))
    item = f.request('sources/' + ids['source_primary'])
    assert item['data']['enabled']
    stream, _ = p.socks_connect(f.SOCKS, target_port=9001)
    process = None
    changed = False
    try:
        command = ['docker', 'exec', '-i', '--user', 'postgres', 'relayweave-database-1',
                   'psql', '-X', '-q', '-A', '-t', '-U', 'relayweave', '-d', 'relayweave', '-v', 'ON_ERROR_STOP=1']
        process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
        # Same session lock used by apply. No table data or unrelated sessions
        # change; statement timeouts and the queued unlock bound its lifetime.
        sql = ("SET statement_timeout='20s';\nSELECT pg_advisory_lock(723501991);\n"
               "\\echo RELAYWEAVE_LOCK_HELD\nSELECT pg_sleep(12);\nSELECT pg_advisory_unlock(723501991);\n\\q\n")
        process.stdin.write(sql.encode())
        process.stdin.flush()
        selector = selectors.DefaultSelector()
        selector.register(process.stdout, selectors.EVENT_READ)
        captured = b''
        deadline = time.monotonic() + 25
        while b'RELAYWEAVE_LOCK_HELD\n' not in captured:
            if time.monotonic() >= deadline or process.poll() is not None:
                raise AssertionError('Configuration-lock fixture did not start')
            for key, _ in selector.select(.2):
                captured += os.read(key.fd, 4096)
        selector.close()
        started = time.monotonic()
        f.patch(item, {'enabled': False})
        record['source_disable_request_seconds'] = time.monotonic() - started
        changed = True
        blocked = None
        while time.monotonic() - started < 5:
            if not tcp_works(f.SOCKS):
                blocked = time.monotonic() - started
                break
            time.sleep(.1)
        record.update(admission_blocked_seconds=blocked, passed=blocked is not None and blocked <= 5)
        echo(stream, b'contention-existing-session')
        assert tcp_works(f.SOCKS_AUTH, ('lab-user', 'lab-pass'))
        record.update(existing_tcp_drains=True, other_source_unaffected=True)
    except Exception as error:
        record['error'] = repr(error)
    finally:
        try:
            if process is not None:
                process.communicate(timeout=35)
                assert process.returncode == 0, 'Own lock-holder session failed'
            if changed:
                until(lambda: not tcp_works(f.SOCKS), 30)
                echo(stream, b'after-lock-release')
                f.patch(f.request('sources/' + item['id']), {'enabled': True})
                until(lambda: tcp_works(f.SOCKS), 30)
                assert p.proxy_http(f.HTTP)[0] == 200
                f.udp_echo(f.SOCKS)
            record['cleanup_restored_traffic'] = True
        except Exception as error:
            record.update(cleanup_error=repr(error), passed=False)
        finally:
            stream.close()
    return record


def check_existing_canary():
    record = {'case': 'changed_health_target_on_existing_ready_path', 'passed': False}
    policy = f.request('policies/' + ids['policy_secondary'])
    original = dict(policy['data'])
    original.update(probe_url='http://origin:9000/healthz', speed_url='http://origin:9000/bytes?n=8388608')
    stream, _ = p.socks_connect(f.SOCKS_AUTH, target_port=9001, auth=('lab-user', 'lab-pass'))
    try:
        assert tcp_works(f.SOCKS_AUTH, ('lab-user', 'lab-pass'))
        f.patch(policy, {'probe_url': 'http://origin:9000/invalid-existing-path-canary', 'probe_fallback_urls': [], 'speed_url': ''})
        operation = f.request('config/apply', 'POST', {}, expected=202)
        started = time.monotonic()
        while time.monotonic() - started < 210:
            current = f.request('operations/' + operation['id'])['data']
            if current['status'] in ('succeeded', 'failed'):
                break
            echo(stream, b'during-candidate-validation')
            time.sleep(.2)
        else:
            raise AssertionError('Candidate operation did not terminate')
        record.update(operation_status=current['status'], operation_error=current.get('error'), elapsed_seconds=time.monotonic() - started)
        record['passed'] = current['status'] == 'failed'
        echo(stream, b'existing-path-after-bad-canary')
        assert tcp_works(f.SOCKS)
        record['existing_tcp_drains'] = True
        record['other_source_unaffected'] = True

        # The rejected draft is still present in the editor. A routine source
        # refresh must compile the recovered health settings, not retry that
        # rejected target and prevent the catalogue's next TTL renewal.
        before = {x['id'] for x in f.request('operations?limit=1000')['items']}
        f.operation('sources/' + ids['source_primary'] + '/refresh')
        deadline = time.monotonic() + 210
        reconciled = None
        while time.monotonic() < deadline:
            candidates = [x for x in f.request('operations?limit=1000')['items']
                          if x['id'] not in before and x['data']['action'] == 'reconcile'
                          and x['data']['target'] == ids['source_primary']]
            completed = [x for x in candidates if x['data']['status'] in ('succeeded', 'failed', 'cancelled')]
            if completed:
                reconciled = completed[0]
                break
            echo(stream, b'during-recovered-source-refresh')
            time.sleep(.2)
        assert reconciled and reconciled['data']['status'] == 'succeeded', 'Source reconciliation retried the rejected health target'
        assert tcp_works(f.SOCKS_AUTH, ('lab-user', 'lab-pass'))
        record['automatic_refresh_uses_recovered_health_targets'] = True
    except Exception as error:
        record.update(error=repr(error), passed=False)
    finally:
        try:
            f.patch(f.request('policies/' + policy['id']), original)
            f.operation('config/apply', timeout=210)
            assert tcp_works(f.SOCKS_AUTH, ('lab-user', 'lab-pass'))
            f.udp_echo(f.SOCKS_AUTH, ('lab-user', 'lab-pass'))
            record['cleanup_restored_traffic'] = True
        except Exception as error:
            record.update(cleanup_error=repr(error), passed=False)
        finally:
            stream.close()
    return record


try:
    checks.append(check_contention())
    print(json.dumps(checks[-1]), flush=True)
    # A failed cleanup is preserved for diagnosis rather than compounding it.
    if checks[-1].get('cleanup_restored_traffic'):
        checks.append(check_existing_canary())
        print(json.dumps(checks[-1]), flush=True)
finally:
    report = {'source': source.name, 'passed': len(checks) == 2 and all(x['passed'] for x in checks), 'checks': checks,
              'scope': 'Own synthetic Docker stack; bounded apply-lock contention and real invalid HTTP canary'}
    report_path.write_text(json.dumps(report, indent=2))
    print(json.dumps({'passed': report['passed'], 'report': str(report_path)}), flush=True)
if not report['passed']:
    raise SystemExit(1)
