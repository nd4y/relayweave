"""Paired, alternating baseline/gateway measurements through identical VLESS TLS paths."""
import argparse
import base64
import concurrent.futures
import json
import math
import socket
import statistics
import time
from pathlib import Path

from acceptance import HOST, response, socks_connect

def transfer(mode, port, size, auth=None):
    started = time.perf_counter()
    if mode == 'socks5':
        sock, _ = socks_connect(port,auth=auth)
        request = f'GET /bytes?n={size} HTTP/1.1\r\nHost: origin\r\nConnection: close\r\n\r\n'
    else:
        sock = socket.create_connection((HOST, port), timeout=15)
        request = f'GET http://origin:9000/bytes?n={size} HTTP/1.1\r\nHost: origin\r\nConnection: close\r\n\r\n'
        if auth:
            value=base64.b64encode((':'.join(auth)).encode()).decode()
            request=request[:-2]+f'Proxy-Authorization: Basic {value}\r\n\r\n'
    try:
        sock.sendall(request.encode())
        r = response(sock)
        assert r.status == 200
        first_byte = time.perf_counter()-started
        count = 0
        while True:
            data = r.read(128*1024)
            if not data:
                break
            count += len(data)
            if data.count(b'x') != len(data):
                raise AssertionError('corrupted body')
        assert count == size
        return {'bytes': count, 'first_byte_seconds': first_byte, 'seconds': time.perf_counter()-started}
    finally:
        sock.close()

def measure(mode, port, size, parallel,auth=None):
    start = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=parallel) as executor:
        flows = list(executor.map(lambda _: transfer(mode, port, size,auth), range(parallel)))
    seconds = time.perf_counter()-start
    return {'seconds': seconds, 'bytes': size*parallel, 'mbps': size*parallel*8/seconds/1e6,
            'first_byte_seconds': [f['first_byte_seconds'] for f in flows], 'flows': flows}

def percentile(xs, q):
    return sorted(xs)[max(0, math.ceil(len(xs)*q)-1)]

def main():
    p = argparse.ArgumentParser()
    p.add_argument('--mib', type=int, default=64)
    p.add_argument('--trials', type=int, default=5)
    p.add_argument('--output', default='/reports/benchmark.json')
    p.add_argument('--full',action='store_true',help='Full control-plane/worker stack, secondary auth on both baseline and app')
    args = p.parse_args()
    groups = []
    for route, offset in [('primary', 0), ('secondary', 1)]:
        auth=('lab-user','lab-pass') if args.full and offset else None
        for mode in ['socks5', 'http']:
            ports = {'baseline': 29101+offset if mode == 'socks5' else 29201+offset,
                     'gateway': 21082+offset if mode == 'socks5' else 28082+offset}
            if args.full:ports['gateway']=(41080+offset if mode=='socks5' else 48080+offset)
            for parallel in [1, 4]:
                for port in ports.values():
                    measure(mode, port, 1024*1024, parallel,auth)
                samples = []
                for trial in range(args.trials):
                    order = ['baseline', 'gateway'] if trial % 2 == 0 else ['gateway', 'baseline']
                    pair = {'trial': trial, 'order': order}
                    for variant in order:
                        pair[variant] = measure(mode, ports[variant], args.mib*1024*1024, parallel,auth)
                    pair['ratio'] = pair['gateway']['mbps']/pair['baseline']['mbps']
                    samples.append(pair)
                    print(json.dumps({'route': route, 'protocol': mode, 'parallel': parallel,
                                      'trial': trial, 'ratio': pair['ratio'],
                                      'baseline_mbps': pair['baseline']['mbps'],
                                      'gateway_mbps': pair['gateway']['mbps']}), flush=True)
                median = {variant: statistics.median(s[variant]['mbps'] for s in samples)
                          for variant in ['baseline', 'gateway']}
                ratio = median['gateway']/median['baseline']
                latency = {variant: percentile([x for s in samples for x in s[variant]['first_byte_seconds']], .95)
                           for variant in ['baseline', 'gateway']}
                groups.append({'route': route, 'protocol': mode, 'parallel': parallel, 'median_mbps': median,
                               'median_ratio': ratio, 'paired_ratio_median': statistics.median(s['ratio'] for s in samples),
                               'p95_first_byte_seconds': latency, 'throughput_gate_passed': ratio >= .90,
                               'samples': samples})
    report = {'timestamp': time.time(), 'mib_per_stream': args.mib, 'trials': args.trials, 'groups': groups,
              'passed': all(g['throughput_gate_passed'] for g in groups),
              'scope': 'Synthetic Docker bridge/NAT + real VLESS TLS. Same protocol/version/limits/upstream. No WAN claim.',
              'limitations': ['shared Docker host', 'Python client and origin may limit measured throughput',
                              'first byte includes TLS upstream and scheduling; not isolated gateway dial latency',
                              'not a 5000-connection or 72-hour acceptance run']}
    Path(args.output).write_text(json.dumps(report, indent=2)+'\n')
    print(json.dumps({'passed': report['passed'], 'summary': [{k: g[k] for k in ['route', 'protocol', 'parallel', 'median_mbps', 'median_ratio']} for g in groups]}), flush=True)
    raise SystemExit(0 if report['passed'] else 1)

if __name__ == '__main__':
    main()
