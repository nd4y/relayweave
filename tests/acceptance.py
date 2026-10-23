"""Real protocol checks through Docker-published ports; stdlib only."""
import argparse
import base64
import concurrent.futures
import hashlib
import http.client
import json
import os
import socket
import ssl
import struct
import time
import traceback
import urllib.request
from pathlib import Path

HOST = '127.0.0.1'
STATUS = 'http://127.0.0.1:29090/status'

def exact(sock, n):
    data = bytearray()
    while len(data) < n:
        part = sock.recv(n-len(data))
        if not part:
            raise EOFError('premature EOF')
        data.extend(part)
    return bytes(data)

def address(host, port):
    try:
        raw = socket.inet_pton(socket.AF_INET, host)
        data = b'\x01'+raw
    except OSError:
        try:
            raw = socket.inet_pton(socket.AF_INET6, host)
            data = b'\x04'+raw
        except OSError:
            raw = host.encode('ascii')
            data = bytes([3, len(raw)])+raw
    return data+struct.pack('!H', port)

def read_address(sock):
    kind = exact(sock, 1)[0]
    if kind == 1:
        raw = exact(sock, 4)
        host = socket.inet_ntop(socket.AF_INET, raw)
    elif kind == 4:
        raw = exact(sock, 16)
        host = socket.inet_ntop(socket.AF_INET6, raw)
    elif kind == 3:
        host = exact(sock, exact(sock, 1)[0]).decode('ascii')
    else:
        raise AssertionError('invalid address type')
    return host, struct.unpack('!H', exact(sock, 2))[0]

def socks_connect(port, target='origin', target_port=9000, auth=None, command=1, expected=0):
    sock = socket.create_connection((HOST, port), timeout=8)
    try:
        method = 2 if auth else 0
        sock.sendall(bytes([5, 1, method]))
        assert exact(sock, 2) == bytes([5, method])
        if auth:
            user, password = (v.encode() for v in auth)
            sock.sendall(bytes([1, len(user)])+user+bytes([len(password)])+password)
            assert exact(sock, 2) == b'\x01\x00'
        sock.sendall(bytes([5, command, 0])+address(target, target_port))
        header = exact(sock, 3)
        assert header == bytes([5, expected, 0]), header
        bound = read_address(sock)
        return sock, bound
    except BaseException:
        sock.close()
        raise

def response(sock, method='GET'):
    reply = http.client.HTTPResponse(sock, method=method)
    reply.begin()
    return reply

def proxy_http(port, path='/healthz', auth=None, payload=None, extra=''):
    method = 'POST' if payload is not None else 'GET'
    with socket.create_connection((HOST, port), timeout=10) as sock:
        headers = f'{method} http://origin:9000{path} HTTP/1.1\r\nHost: origin:9000\r\nConnection: close\r\n'
        if auth:
            headers += 'Proxy-Authorization: Basic '+base64.b64encode((':'.join(auth)).encode()).decode()+'\r\n'
        if payload is not None:
            headers += f'Content-Length: {len(payload)}\r\n'
        sock.sendall((headers+extra+'\r\n').encode()+(payload or b''))
        r = response(sock, method)
        return r.status, dict(r.getheaders()), r.read()

def socks_http(port, path='/healthz', auth=None):
    sock, _ = socks_connect(port, auth=auth)
    with sock:
        sock.sendall(f'GET {path} HTTP/1.1\r\nHost: origin\r\nConnection: close\r\n\r\n'.encode())
        r = response(sock)
        return r.status, dict(r.getheaders()), r.read()

def json_url(url=STATUS):
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(url, timeout=5) as r:
        return json.load(r)

def wait_for(predicate, seconds=15):
    deadline = time.monotonic()+seconds
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.1)
    raise AssertionError('deadline exceeded')

def datagram_payload(data):
    assert data[:3] == b'\0\0\0'
    kind = data[3]
    offset = {1: 8, 4: 20}.get(kind)
    if kind == 3:
        offset = 5+data[4]
    assert offset is not None
    return data[offset+2:]

def udp_roundtrip(port=21082, auth=None):
    control, bound = socks_connect(port, '0.0.0.0', 0, auth, command=3)
    assert bound[0] == HOST and 30000 <= bound[1] <= 30063, bound
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as udp:
            udp.settimeout(5)
            # Xray drops zero-payload UDP; exercise non-empty datagrams, including beyond MTU.
            for length in [1, 64, 1200, 4096, 7000]:
                payload = os.urandom(length)
                udp.sendto(b'\0\0\0'+address('origin', 9002)+payload, bound)
                received, _ = udp.recvfrom(65535)
                assert datagram_payload(received) == payload
            # A syntactically valid DNS query and response transported through VLESS UDP.
            query = b'\xca\xfe\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x04test\x00\x00\x01\x00\x01'
            udp.sendto(b'\0\0\0'+address('origin', 9003)+query, bound)
            dns = datagram_payload(udp.recvfrom(4096)[0])
            assert dns[:2] == b'\xca\xfe' and dns[-4:] == b'\x7f\0\0\x2a'
            drops = json_url()['udp_drops']
            udp.sendto(b'\0\0\1'+address('origin', 9002)+b'fragment', bound)
            udp.sendto(b'\0\0\0'+address('origin', 9002)+os.urandom(16000), bound)
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as impostor:
                impostor.settimeout(0.3)
                impostor.sendto(b'\0\0\0'+address('origin', 9002)+b'spoof', bound)
                try:
                    impostor.recvfrom(4096)
                    raise AssertionError('foreign UDP endpoint accepted')
                except socket.timeout:
                    pass
            wait_for(lambda: json_url()['udp_drops'] >= drops+3)
    finally:
        control.close()
    wait_for(lambda: json_url()['udp_associations'] == 0)

def tcp_halfclose(port=21082):
    data = os.urandom(128*1024)
    sock, _ = socks_connect(port, target_port=9001)
    with sock:
        sock.sendall(data)
        sock.shutdown(socket.SHUT_WR)
        received = bytearray()
        while True:
            part = sock.recv(65536)
            if not part:
                break
            received.extend(part)
        assert received == data

def http_tls(port=28080):
    with socket.create_connection((HOST, port), timeout=10) as sock:
        sock.sendall(b'CONNECT origin:9443 HTTP/1.1\r\nHost: origin:9443\r\n\r\n')
        # Do not allow a buffered HTTP parser to prefetch TLS bytes.
        header = bytearray()
        while not header.endswith(b'\r\n\r\n'):
            header.extend(exact(sock, 1))
        assert header.startswith(b'HTTP/1.1 200 '), header
        context = ssl.create_default_context(cafile='/lab/ca.pem')
        with context.wrap_socket(sock, server_hostname='origin') as secure:
            secure.sendall(b'GET /healthz HTTP/1.1\r\nHost: origin\r\nConnection: close\r\n\r\n')
            r = response(secure)
            assert r.status == 200 and r.read() == b'ok\n'

def auth_failures():
    assert proxy_http(28081)[0] == 407
    assert proxy_http(28081, auth=('lab-user', 'wrong'))[0] == 407
    with socket.create_connection((HOST, 21081), timeout=3) as s:
        s.sendall(b'\x05\x01\x00')
        assert exact(s, 2) == b'\x05\xff'
    with socket.create_connection((HOST, 21081), timeout=3) as s:
        s.sendall(b'\x05\x01\x02')
        assert exact(s, 2) == b'\x05\x02'
        s.sendall(b'\x01\x08lab-user\x05wrong')
        assert exact(s, 2) == b'\x01\x01'

def udp_exhaustion():
    controls = []
    try:
        for _ in range(64):
            control, _ = socks_connect(21082, '0.0.0.0', 0, command=3)
            controls.append(control)
        extra, _ = socks_connect(21082, '0.0.0.0', 0, command=3, expected=1)
        extra.close()
        assert json_url()['udp_associations'] == 64
    finally:
        for c in controls:
            c.close()
    wait_for(lambda: json_url()['udp_associations'] == 0)

def health_checks():
    status = json_url()
    assert len(status['cores']) == 2
    assert {c['version'].split()[1] for c in status['cores']} == {'26.10.17', '26.9.30'}
    assert all(c['ready'] and c['pid'] > 1 and len(c['sha256']) == 64 for c in status['cores'])
    return status

def headers_and_body():
    data = os.urandom(1024*1024)
    code, _, body = proxy_http(28081, '/echo', auth=('lab-user', 'lab-pass'), payload=data,
                               extra='Connection: X-Hop, close\r\nX-Hop: must-not-leak\r\n')
    result = json.loads(body)
    assert code == 200 and result['bytes'] == len(data)
    assert result['sha256'] == hashlib.sha256(data).hexdigest()
    assert not result['proxy_authorization'] and not result['hop_header']

def main():
    p = argparse.ArgumentParser()
    p.add_argument('--output', default='/reports/acceptance.json')
    args = p.parse_args()
    results = []
    checks = [
        ('two_concurrent_real_core_versions', health_checks),
        ('http_forward_noauth', lambda: assert_http(proxy_http(28080))),
        ('http_forward_auth', lambda: assert_http(proxy_http(28081, auth=('lab-user', 'lab-pass')))),
        ('http_connect_verified_tls', http_tls),
        ('socks5_primary_tcp', lambda: assert_http(socks_http(21082))),
        ('socks5_secondary_tcp', lambda: assert_http(socks_http(21083))),
        ('socks5_authenticated_tcp', lambda: assert_http(socks_http(21081, auth=('lab-user', 'lab-pass')))),
        ('authentication_rejections', auth_failures),
        ('http_body_and_hop_header_isolation', headers_and_body),
        ('http_chunked_response', lambda: assert_chunked(proxy_http(28080, '/chunked'))),
        ('socks5_primary_udp_dns_nat', udp_roundtrip),
        ('socks5_secondary_udp_dns_nat', lambda: udp_roundtrip(21083)),
        ('socks5_authenticated_udp', lambda: udp_roundtrip(21081, ('lab-user', 'lab-pass'))),
        ('tcp_halfclose_primary', tcp_halfclose),
        ('tcp_halfclose_secondary', lambda: tcp_halfclose(21083)),
        ('unsupported_bind_rejected', lambda: close_socks(socks_connect(21080, command=2, expected=7))),
        ('udp_budget_exhaustion_and_cleanup', udp_exhaustion),
        ('concurrent_round_robin', concurrency),
        ('standalone_primary_tcp', lambda: assert_http(socks_http(29101))),
        ('standalone_secondary_tcp', lambda: assert_http(socks_http(29102))),
        ('metrics_export', metrics_check),
    ]
    for name, fn in checks:
        started = time.monotonic()
        try:
            fn()
            row = {'name': name, 'passed': True, 'seconds': time.monotonic()-started}
        except Exception as exc:
            row = {'name': name, 'passed': False, 'seconds': time.monotonic()-started,
                   'error': f'{type(exc).__name__}: {exc}', 'traceback': traceback.format_exc()}
        results.append(row)
        print(json.dumps({k: v for k, v in row.items() if k != 'traceback'}), flush=True)
    try:
        status = json_url()
    except Exception as exc:
        status = {'error': str(exc)}
    report = {'timestamp': time.time(), 'checks': results, 'passed': all(x['passed'] for x in results),
              'status': status, 'environment': {'client': 'Docker host network', 'path': 'published port -> worker -> loopback Xray -> VLESS TLS -> isolated origin'}}
    Path(args.output).write_text(json.dumps(report, indent=2)+'\n')
    raise SystemExit(0 if report['passed'] else 1)

def assert_http(result):
    code, headers, body = result
    assert code == 200 and body == b'ok\n', (code, body)
    assert headers.get('X-Lab-Origin') == 'isolated'

def assert_chunked(result):
    assert result[0] == 200 and result[2] == b'chunk one\nchunk two\n'

def close_socks(result):
    result[0].close()

def concurrency():
    with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
        list(pool.map(lambda _: assert_http(socks_http(21080)), range(64)))
    assert all(r['connections'] > 0 for r in json_url()['routes'])

def metrics_check():
    with urllib.request.urlopen('http://127.0.0.1:29090/metrics', timeout=3) as response:
        text = response.read().decode()
    assert 'proxy_core_ready{core="primary",version="26.10.17"} 1' in text
    assert 'proxy_core_ready{core="secondary",version="26.9.30"} 1' in text
    assert 'lab-pass' not in text and '00000000-0000' not in text

if __name__ == '__main__':
    main()
