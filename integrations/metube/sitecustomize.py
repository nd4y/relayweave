"""MeTube integration: keep each extraction/download on one healthy IP path.

Activated only by RELAYWEAVE_DOWNLOAD_PATHS. Mounted read-only into the official
MeTube image; no media, credentials or URL is logged by this module.
"""
import json
import os
import secrets
import time
import urllib.parse
import urllib.request

_paths = json.loads(os.environ.get('RELAYWEAVE_DOWNLOAD_PATHS', '[]'))
_cache = None
_cache_at = 0.0
_opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def _snapshot():
    global _cache, _cache_at
    now = time.monotonic()
    if _cache is not None and now - _cache_at < 1:
        return _cache
    try:
        with _opener.open('http://relayweave-proxy:9090/status', timeout=2) as response:
            data = response.read(1 << 20)
        status = json.loads(data)
        assert not status['drained']
        _cache, _cache_at = status, now
        return status
    except Exception as exc:
        if _cache is not None and now - _cache_at < 5:
            return _cache
        raise RuntimeError('RelayWeave route status unavailable') from None


def _route(params):
    params = dict(params or {})
    if not _paths or params.get('_relayweave_path_selected'):
        return params
    original = params.get('proxy') or ''
    proxy = urllib.parse.urlsplit(original)
    if proxy.hostname != 'relayweave-proxy' or proxy.port != 8082:
        return params
    if not proxy.username or not proxy.password:
        raise RuntimeError('RelayWeave download paths require authentication')
    status = _snapshot()
    policies = {p['id']: p for p in status['policies']}
    candidates = []
    for path in _paths:
        port = int(path['port'])
        if not 1024 <= port <= 65535:
            raise RuntimeError('Invalid RelayWeave path port')
        ready = set(policies.get(path['policy_id'], {}).get('ready_paths', []))
        if not ready:
            continue
        routes = [r for r in status['routes'] if r['profile_id'] in ready]
        addresses = frozenset(ip for r in routes for ip in (r.get('server_ips') or []))
        load = sum(r.get('active', 0) for r in routes)
        candidates.append({'port': port, 'addresses': addresses, 'load': load})
    if not candidates:
        raise RuntimeError('No healthy RelayWeave download path')
    # If formerly distinct paths converge onto the same balancer, account for
    # their shared load instead of treating them as independent capacity.
    for candidate in candidates:
        candidate['shared_load'] = sum(other['load'] for other in candidates
            if other is candidate or candidate['addresses'] & other['addresses'])
    best = min(c['shared_load'] for c in candidates)
    chosen = secrets.choice([c for c in candidates if c['shared_load'] == best])
    credentials = proxy.netloc.rsplit('@', 1)[0]
    params['proxy'] = urllib.parse.urlunsplit((proxy.scheme,
        credentials + '@relayweave-proxy:' + str(chosen['port']), proxy.path, proxy.query, proxy.fragment))
    params['_relayweave_path_selected'] = True
    return params


if _paths:
    import dl_formats
    import yt_dlp

    _original_opts = dl_formats.get_opts
    _original_init = yt_dlp.YoutubeDL.__init__

    def _get_opts(*args, **kwargs):
        # Choose before MeTube installs its socket guard, so the exact selected
        # proxy port is included in the existing private-service allowlist.
        return _route(_original_opts(*args, **kwargs))

    def _init(self, params=None, *args, **kwargs):
        # Initial metadata extraction also needs a stable path. Download params
        # already marked by get_opts retain that same path and guard exception.
        return _original_init(self, _route(params), *args, **kwargs)

    dl_formats.get_opts = _get_opts
    yt_dlp.YoutubeDL.__init__ = _init
    HOOK_ACTIVE = True
