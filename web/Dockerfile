FROM node:26-bookworm-slim@sha256:298992629d69bbd5b2b03ad57094d8e4fcb3a8eeb7c4e25d29de656519568913 AS build
WORKDIR /app
ARG VITE_GRAFANA_URL=""
ENV VITE_GRAFANA_URL=$VITE_GRAFANA_URL
ENV NODE_OPTIONS=--max-old-space-size=768
COPY package*.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN node collect-notices.mjs && npm run build
FROM nginxinc/nginx-unprivileged:1.30.6-alpine-slim@sha256:5d8bb9437d74a600ec5dbf90c035451c724dc33eac8d3a71078f6f8612c345f6
COPY nginx.conf /etc/relayweave/server.conf
COPY --chmod=0755 start-nginx.sh /usr/local/bin/relayweave-web
COPY --from=build /app/dist /usr/share/nginx/html
USER 101
ENV UI_LISTEN_PORT=18443
EXPOSE 18443
ENTRYPOINT ["/usr/local/bin/relayweave-web"]
CMD []
