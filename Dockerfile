# syntax=docker/dockerfile:1

# Build stage. CGO stays off so the result runs on a distroless static base (§4.3).
FROM golang:1.27-alpine AS build

ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Dependency layer caches separately from source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags "-s -w \
      -X github.com/teagan42/metricule/internal/version.Version=${VERSION} \
      -X github.com/teagan42/metricule/internal/version.Commit=${COMMIT} \
      -X github.com/teagan42/metricule/internal/version.Date=${DATE}" \
    -o /out/metricule ./cmd/metricule

# Runtime stage. Static distroless: no shell, no package manager, nonroot by default.
FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

LABEL org.opencontainers.image.title="Metricule" \
      org.opencontainers.image.description="Declarative Prometheus exporter for existing data sources" \
      org.opencontainers.image.source="https://github.com/teagan42/metricule" \
      org.opencontainers.image.documentation="https://github.com/teagan42/metricule/blob/main/README.md" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${DATE}"

COPY --from=build /out/metricule /usr/local/bin/metricule

# 65532 is distroless `nonroot`. Mounted SQLite files must be readable by this UID.
USER 65532:65532

EXPOSE 9108

ENTRYPOINT ["/usr/local/bin/metricule"]
CMD ["serve", "--config", "/etc/metricule/config.yaml"]
