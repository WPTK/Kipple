# syntax=docker/dockerfile:1

# --- frontend build -----------------------------------------------------
FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci
COPY web/ ./
RUN npm run build

# --- go build -------------------------------------------------------------
FROM golang:1.27-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/embed.go
COPY --from=web /app/web/dist ./web/dist

ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOFLAGS=-trimpath \
    go build -ldflags="-s -w -X main.version=${VERSION}" -o /kipple ./cmd/kipple

# distroless has no shell to mkdir/chown at runtime, so pre-create /data
# here, owned by the nonroot image's uid/gid (65532), and copy it over.
RUN mkdir -p /data && chown 65532:65532 /data

# --- runtime ----------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ARG VCS_REF=unknown
LABEL org.opencontainers.image.title="Kipple" \
      org.opencontainers.image.description="Self-hosted RSS reader with a Google Reader API" \
      org.opencontainers.image.source="https://github.com/WPTK/Kipple" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.licenses="NOASSERTION"
COPY --from=build /kipple /kipple
COPY --from=build --chown=65532:65532 /data /data
COPY --chown=65532:65532 LICENSE THIRD_PARTY_NOTICES.md /licenses/
EXPOSE 7080
VOLUME /data
# Runs the binary's own probe (no shell or curl in distroless).
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s --retries=3 CMD ["/kipple","healthcheck"]
STOPSIGNAL SIGTERM
ENTRYPOINT ["/kipple"]
CMD ["serve"]
