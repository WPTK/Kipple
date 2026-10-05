# syntax=docker/dockerfile:1

# Base images are pinned by tag and digest; Dependabot (docker ecosystem) bumps both together.

# --- frontend build -----------------------------------------------------
FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402 AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --ignore-scripts
COPY web/ ./
# The changelog feeds the "What's new" panel; the build id and version are stamped into the bundle.
COPY CHANGELOG.md /app/CHANGELOG.md
ARG VERSION=dev
ARG SOURCE_DATE_EPOCH
RUN npm run build

# --- go build -------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY starter/ ./starter/
COPY web/embed.go ./web/embed.go
COPY --from=web /app/web/dist ./web/dist

# VERSION and VCS_REF come from the build (compose build.args, CI). .git is not in the build context
# (.dockerignore), so the image cannot work them out itself; an unset VERSION reports "dev".
ARG VERSION=dev
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown
# Cross-compile on the build platform instead of emulating the target (CGO is off; the SQLite driver is pure Go).
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOFLAGS=-trimpath \
    go build -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${VCS_REF} -X main.buildDate=${BUILD_DATE}" -o /kipple ./cmd/kipple

# distroless has no shell to mkdir/chown at runtime, so pre-create /data
# here, owned by the nonroot image's uid/gid (65532), and copy it over.
RUN mkdir -p /data && chown 65532:65532 /data

# --- runtime ----------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG VERSION=dev
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="Kipple" \
      org.opencontainers.image.description="Self-hosted RSS reader with a Google Reader API" \
      org.opencontainers.image.source="https://github.com/WPTK/Kipple" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.licenses="BlueOak-1.0.0" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.url="https://github.com/WPTK/Kipple" \
      org.opencontainers.image.documentation="https://github.com/WPTK/Kipple#readme"
# org.opencontainers.image.base.name and .base.digest are deliberately not written here: they would repeat the FROM
# line above and go stale on the first Dependabot bump. The release workflow reads them from that line
# (scripts/release-tags.sh base-image) and stamps them on the official image; a local build simply has no such label.
COPY --from=build /kipple /kipple
COPY --from=build --chown=65532:65532 /data /data
COPY --chown=65532:65532 LICENSE THIRD_PARTY_NOTICES.md /licenses/
EXPOSE 1919
VOLUME /data
# Runs the binary's own probe (no shell or curl in distroless).
HEALTHCHECK --interval=30s --timeout=5s --start-period=40s --retries=3 CMD ["/kipple","healthcheck"]
STOPSIGNAL SIGTERM
ENTRYPOINT ["/kipple"]
CMD ["serve"]
