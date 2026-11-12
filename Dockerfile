# syntax=docker/dockerfile:1

# Frontend: built once on the build platform, embedded into every arch.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/mybuilds ./cmd/server

# distroless has no shell, so the data directory is prepared here and copied
# in with the nonroot owner (uid 65532).
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mybuilds /mybuilds
COPY --from=build --chown=nonroot:nonroot /out/data /data

ENV MYBUILDS_LISTEN=:8080 \
    MYBUILDS_METRICS_LISTEN=:9090 \
    MYBUILDS_DB_PATH=/data/mybuilds.db \
    RELEASE_ID=unknown

EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/mybuilds"]
