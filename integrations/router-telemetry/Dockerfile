FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
WORKDIR /src
ENV GOMAXPROCS=1 GOFLAGS=-p=1 GOMEMLIMIT=512MiB
COPY go.mod go.sum ./
COPY internal/routertelemetry ./internal/routertelemetry
COPY cmd/router-telemetry ./cmd/router-telemetry
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/router-telemetry ./cmd/router-telemetry

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/router-telemetry /app/router-telemetry
USER 65532:65532
ENTRYPOINT ["/app/router-telemetry"]
