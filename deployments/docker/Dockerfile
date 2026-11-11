# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26

# ---------- build ----------
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Dependency cache is kept separate from the code so rebuilds are fast.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w \
        -X github.com/fiankasepmantea/go-gin-tenant-template/internal/version.Version=${VERSION} \
        -X github.com/fiankasepmantea/go-gin-tenant-template/internal/version.Commit=${COMMIT}" \
      -o /out/server ./cmd/server
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w \
        -X github.com/fiankasepmantea/go-gin-tenant-template/internal/version.Version=${VERSION} \
        -X github.com/fiankasepmantea/go-gin-tenant-template/internal/version.Commit=${COMMIT}" \
      -o /out/auth-grpc ./cmd/grpc

# ---------- runtime: no shell, non-root ----------
FROM gcr.io/distroless/static-debian12:nonroot
# The same image holds the gRPC AuthService: run it with  entrypoint: ["/auth-grpc"]  (see docs/grpc.md).
COPY --from=build /out/server /server
COPY --from=build /out/auth-grpc /auth-grpc
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/server"]
