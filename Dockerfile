# Build stage
FROM golang:1.24.5-alpine AS builder

WORKDIR /app

COPY tee-proxy/ ./tee-proxy
COPY tee-node/ ./tee-node

WORKDIR /app/tee-proxy

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux go build -a -o main ./cmd/proxy

# Final stage
FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/tee-proxy/main .

COPY tee-proxy/config/config.example.toml ./config/config.toml

# Create non-root user and change ownership
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

RUN chown -R appuser:appgroup /app

USER appuser

EXPOSE 6661
EXPOSE 6662

CMD ["./main"]