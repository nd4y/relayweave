#!/bin/sh
set -eu
ui_port=${UI_LISTEN_PORT:-18443}
api_port=${API_UPSTREAM_PORT:-8080}
api_host=${API_UPSTREAM_HOST:-control-plane}
case "$ui_port:$api_port" in *[!0-9:]*|:*|*:) exit 64;; esac
test "$ui_port" -gt 0 && test "$ui_port" -le 65535
test "$api_port" -gt 0 && test "$api_port" -le 65535
case "$api_host" in ''|*[!a-zA-Z0-9_.-]*) exit 64;; esac
sed -e "s/__UI_PORT__/$ui_port/g" -e "s/__API_HOST__/$api_host/g" -e "s/__API_PORT__/$api_port/g" /etc/relayweave/server.conf > /tmp/relayweave-server.conf
cat > /tmp/relayweave-nginx.conf <<'NGINX'
worker_processes auto;
pid /tmp/nginx.pid;
error_log /dev/stderr warn;
events { worker_connections 1024; }
http {
 include /etc/nginx/mime.types;
 default_type application/octet-stream;
 access_log off;
 sendfile on;
 keepalive_timeout 65;
 client_body_temp_path /tmp/client_temp;
 proxy_temp_path /tmp/proxy_temp;
 fastcgi_temp_path /tmp/fastcgi_temp;
 uwsgi_temp_path /tmp/uwsgi_temp;
 scgi_temp_path /tmp/scgi_temp;
 include /tmp/relayweave-server.conf;
}
NGINX
exec nginx -c /tmp/relayweave-nginx.conf -g 'daemon off;'
