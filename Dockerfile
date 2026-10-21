FROM --platform=$BUILDPLATFORM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
ARG TARGETARCH
WORKDIR /src
ENV GOMAXPROCS=2 GOFLAGS=-p=1 GOMEMLIMIT=512MiB
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,id=relayweave-go-build,target=/root/.cache/go-build,sharing=locked CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/proxy-worker ./cmd/proxy-worker
RUN --mount=type=cache,id=relayweave-go-build,target=/root/.cache/go-build,sharing=locked CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/control-plane ./cmd/control-plane
RUN --mount=type=cache,id=relayweave-go-build,target=/root/.cache/go-build,sharing=locked CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/artifact-fetch ./cmd/artifact-fetch
COPY artifacts.lock.json /out/artifacts.lock.json
RUN /out/artifact-fetch --lock /out/artifacts.lock.json --version 26.10.17 --root /out/cores --arch $TARGETARCH
COPY tools/collect-notices.go /tmp/relayweave-collect-notices.go
RUN --mount=type=cache,id=relayweave-go-build,target=/root/.cache/go-build,sharing=locked go run /tmp/relayweave-collect-notices.go --arch $TARGETARCH

FROM build AS test
RUN --mount=type=cache,id=relayweave-go-build,target=/root/.cache/go-build,sharing=locked CGO_ENABLED=1 go test -race ./...
RUN --mount=type=cache,id=relayweave-go-build,target=/root/.cache/go-build,sharing=locked go vet ./...

FROM build AS fuzz
RUN --mount=type=cache,id=relayweave-go-build,target=/root/.cache/go-build,sharing=locked go test ./internal/catalog -run='^$' -fuzz=FuzzParse -fuzztime=60s -parallel=2
RUN --mount=type=cache,id=relayweave-go-build,target=/root/.cache/go-build,sharing=locked go test ./internal/socks -run='^$' -fuzz=FuzzDatagram -fuzztime=60s -parallel=2

FROM build AS audit
RUN --mount=type=cache,id=relayweave-go-build,target=/root/.cache/go-build,sharing=locked GOBIN=/out/audit-bin go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
RUN go version > /out/audit-go-version.txt
RUN --mount=type=cache,id=relayweave-go-build,target=/root/.cache/go-build,sharing=locked /out/audit-bin/govulncheck ./... > /out/audit-govulncheck.txt 2>&1 || { cat /out/audit-govulncheck.txt; exit 1; }

FROM scratch AS audit-report
COPY --from=audit /out/audit-go-version.txt /go-version.txt
COPY --from=audit /out/audit-govulncheck.txt /govulncheck.txt

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS runtime
COPY --from=build /out/proxy-worker /app/proxy-worker
COPY --from=build /out/control-plane /app/control-plane
COPY --from=build /out/artifacts.lock.json /app/artifacts.lock.json
COPY --from=build /out/cores/v26.10.17 /opt/xray/builtin
COPY --from=build /out/licenses /usr/share/relayweave/licenses
USER 65532:65532
ENTRYPOINT ["/app/proxy-worker"]
CMD ["serve"]
