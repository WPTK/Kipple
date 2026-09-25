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
COPY --from=build /kipple /kipple
COPY --from=build --chown=65532:65532 /data /data
EXPOSE 7080
VOLUME /data
ENTRYPOINT ["/kipple"]
CMD ["serve"]
