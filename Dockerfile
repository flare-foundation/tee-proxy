# Build stage
FROM golang:1.25.1-alpine AS builder

WORKDIR /app/tee-proxy

# Build metadata stamped into the build_info metric. The build context excludes .git,
# so debug.ReadBuildInfo cannot recover vcs.revision here; CI passes these via --build-arg.
# VERSION defaults to "dev" and is set from the Git tag only for release images.
ARG REVISION=unknown
ARG VERSION=dev

COPY . .
RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-X github.com/flare-foundation/tee-proxy/internal/version.Revision=${REVISION} -X github.com/flare-foundation/tee-proxy/internal/version.Version=${VERSION}" \
    -o main ./cmd/proxy

# Final stage
FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/tee-proxy/main .

COPY config.example.toml ./config/config.toml

# Create non-root user and change ownership
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

RUN chown -R appuser:appgroup /app

USER appuser

EXPOSE 6661
EXPOSE 6662

CMD ["./main"]
