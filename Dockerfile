# Rebound — self-hosted inbox for Resend.
# Stage 1: build the web app (React + Vite) into server/dist.
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN mkdir -p ../server && npx vite build

# Stage 2: build the Go server with the web app embedded.
FROM golang:1.26-alpine AS server
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
COPY --from=web /src/server/dist ./dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /rebound .

# Stage 3: small runtime image, non-root, data in /data.
FROM alpine:3.21
LABEL org.opencontainers.image.title="Rebound" \
      org.opencontainers.image.description="Self-hosted, multi-account inbox for Resend" \
      org.opencontainers.image.source="https://github.com/vrlda/rebound" \
      org.opencontainers.image.licenses="MIT"
RUN apk add --no-cache ca-certificates tzdata wget && adduser -S -u 1002 -D rebound \
    && mkdir -p /data && chown rebound /data
COPY --from=server /rebound /usr/local/bin/rebound
USER rebound
ENV DATA_DIR=/data LISTEN=:8080
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["rebound"]
