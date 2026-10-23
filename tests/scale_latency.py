"""Repeat the same latency gates on an existing 50-source/10k catalogue."""
import json
from pathlib import Path
import full_acceptance as f
from scale import timed

def main():
    f.csrf = f.request('auth/login', 'POST', {'password': f.PASSWORD})['csrf']
    sources = f.request('sources?limit=1000')['items']
    assert len(sources) == 50 and f.request('profiles?limit=1')['total'] == 10000
    pool = f.request('pools')['items'][0]
    results = {}
    for name, path in [('overview', 'overview'), ('profiles_1000', 'profiles?limit=1000&offset=4000'),
                       ('source', 'sources/' + sources[0]['id']), ('pool_preview', 'pools/' + pool['id'] + '/preview')]:
        results[name] = timed(lambda path=path: f.request(path))
        print(name, round(results[name]['p95_ms'], 3), flush=True)
    settings = f.request('settings/global')
    def draft_write():
        nonlocal settings
        settings = f.patch(settings, {'download_mbps': 800})
    results['draft_write'] = timed(draft_write)
    passed = all(v['p95_ms'] <= (500 if k == 'draft_write' else 300) for k, v in results.items())
    report = {'passed': passed, 'sources': 50, 'profiles': 10000, 'latency': results,
              'scope': 'Same existing scale fixture and API latency gate; warmup=3, samples=30.'}
    Path('/reports/scale-latency.json').write_text(json.dumps(report, indent=2))
    print(json.dumps({'passed': passed, 'latency_p95_ms': {k: v['p95_ms'] for k, v in results.items()}}), flush=True)
    assert passed

if __name__ == '__main__': main()
