"""A separate controller kills only the secondary core after this test is armed."""
import json
import time
from pathlib import Path

from acceptance import exact, json_url, socks_connect, socks_http, assert_http

output = Path('/reports/resilience.json')
marker = Path('/reports/resilience-armed')
before = json_url()
secondary = next(c for c in before['cores'] if c['id'] == 'secondary')
sock, _ = socks_connect(21082, target_port=9001)
errors = []
seen_restart = False
count = 0
try:
    marker.write_text('ready\n')
    deadline = time.monotonic()+30
    started = time.monotonic()
    while time.monotonic() < deadline:
        payload = f'primary-session-{count:06d}'.encode()
        sock.sendall(payload)
        assert exact(sock, len(payload)) == payload
        count += 1
        current = json_url()
        after = next(c for c in current['cores'] if c['id'] == 'secondary')
        if after['starts'] > secondary['starts'] and after['ready']:
            seen_restart = True
            assert_http(socks_http(21083))
            if time.monotonic()-started > 3:
                break
        time.sleep(.05)
except Exception as exc:
    errors.append(f'{type(exc).__name__}: {exc}')
finally:
    sock.close()
    marker.unlink(missing_ok=True)
result = {'passed': not errors and seen_restart, 'primary_session_echoes': count,
          'secondary_recovered': seen_restart, 'errors': errors, 'before': before,
          'after': json_url(), 'seconds': time.monotonic()-started}
output.write_text(json.dumps(result, indent=2)+'\n')
print(json.dumps(result), flush=True)
raise SystemExit(0 if result['passed'] else 1)
