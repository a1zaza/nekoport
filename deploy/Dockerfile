FROM node:24-alpine AS frontend-builder
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts
COPY web/ ./
RUN npm run build && test -s dist/index.html

FROM golang:1.26-alpine AS backend-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/*.go ./web/
COPY --from=frontend-builder /app/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -o /app/bin/nekoport ./cmd/nekoport

FROM alpine:3.23
LABEL org.opencontainers.image.title="nekoport" \
    org.opencontainers.image.source="https://github.com/a1zaza/nekoport"
RUN apk add --no-cache ca-certificates tzdata tini && \
    addgroup -S -g 1000 nekoport && \
    adduser -S -D -H -u 1000 -G nekoport nekoport && \
    mkdir -p /var/lib/nekoport && chown nekoport:nekoport /var/lib/nekoport && chmod 0700 /var/lib/nekoport
COPY --from=backend-builder /app/bin/nekoport /usr/local/bin/nekoport
COPY --chmod=0755 deploy/entrypoint.sh /entrypoint.sh
ENV TZ=Asia/Shanghai
WORKDIR /var/lib/nekoport
USER 1000:1000
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=90s --retries=3 CMD ["nekoport", "healthcheck"]
ENTRYPOINT ["/sbin/tini", "-g", "--", "/entrypoint.sh"]
