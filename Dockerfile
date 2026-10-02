# ==========================================
# STAGE 1: COMPILE SERVER SPA FRONTEND (BUN)
# ==========================================
FROM oven/bun:latest AS frontend-builder
WORKDIR /app

# Copy package.json / bun.lock for server
COPY web/server/package.json web/server/bun.lock* ./web/server/
RUN cd web/server && bun install --frozen-lockfile

# Copy frontend source code and compile
COPY web/server ./web/server
RUN cd web/server && bun run build

# ==========================================
# STAGE 2: COMPILE GO BINARIES
# ==========================================
FROM golang:1.26.3 AS go-builder
WORKDIR /app

# Pre-fetch Go dependencies for the main module
COPY go.mod go.sum ./
COPY internal/ehco-patched ./internal/ehco-patched
RUN go mod download

# Pre-fetch Go dependencies for the nested ehco module
RUN cd internal/ehco-patched && go mod download

# Copy the compiled server frontend from Stage 1
COPY --from=frontend-builder /app/web/server/dist ./web/server/dist

# Satisfy go:embed for client dist with a placeholder index
RUN mkdir -p web/client/dist && touch web/client/dist/index.html

# Copy the rest of the Go codebase
COPY . .

# Compile Go backend binary with embedded SPA assets (parallel build across all CPU cores)
RUN CGO_ENABLED=0 GOGC=50 go build -ldflags "-s -w" -o bin/clever-connect main.go

# Compile Ehco binary from its nested module directory (parallel build across all CPU cores)
RUN cd internal/ehco-patched && CGO_ENABLED=0 GOGC=50 go build -ldflags "-s -w" -o /app/bin/ehco ./cmd/ehco

# ==========================================
# STAGE 3: MINIMAL RUNTIME CONTAINER
# ==========================================
FROM ubuntu:latest

# Prevent interactive prompts
ENV DEBIAN_FRONTEND=noninteractive

# Install runtime dependencies ONLY (nginx, ffmpeg for streaming, curl for binary fetching)
RUN apt-get update && apt-get install -y \
    nginx \
    ffmpeg \
    curl \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy compiled binaries from Go Builder Stage
COPY --from=go-builder /app/bin/clever-connect ./bin/clever-connect
COPY --from=go-builder /app/bin/ehco ./bin/ehco
COPY --from=go-builder /app/core ./core

# Download and install Gost (SOCKS5 proxy relayer)
RUN curl -L https://github.com/ginuerzh/gost/releases/download/v2.11.5/gost-linux-amd64-2.11.5.gz | gzip -d > /usr/local/bin/gost && \
    chmod +x /usr/local/bin/gost

# Download and install MediaMTX (WebRTC/HLS streamer)
RUN curl -L https://github.com/bluenviron/mediamtx/releases/download/v1.9.0/mediamtx_v1.9.0_linux_amd64.tar.gz | tar -xz -C /usr/local/bin/ mediamtx && \
    chmod +x /usr/local/bin/mediamtx

# Download and install TrustTunnel endpoint & client (stealth VPN protocol)
RUN curl -fSL https://github.com/TrustTunnel/TrustTunnel/releases/download/v1.0.33/trusttunnel-v1.0.33-linux-x86_64.tar.gz | tar -xz -C ./bin --strip-components=1 trusttunnel-v1.0.33-linux-x86_64/trusttunnel_endpoint && \
    curl -fSL https://github.com/TrustTunnel/TrustTunnelClient/releases/download/v1.0.49/trusttunnel_client-v1.0.49-linux-x86_64.tar.gz | tar -xz -C ./bin --strip-components=1 trusttunnel_client-v1.0.49-linux-x86_64/trusttunnel_client && \
    chmod +x ./bin/trusttunnel_endpoint ./bin/trusttunnel_client

# Copy configuration files
COPY nginx.conf /etc/nginx/nginx.conf
COPY mediamtx.yml /etc/mediamtx.yml

# Create and configure runtime data storage and download GeoIP databases
RUN mkdir -p data/geo && \
    curl -fSL https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-City.mmdb -o data/geo/GeoLite2-City.mmdb && \
    curl -fSL https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-ASN.mmdb -o data/geo/GeoLite2-ASN.mmdb && \
    curl -fSL https://github.com/metowolf/qqwry.dat/releases/latest/download/qqwry.dat -o data/geo/qqwry.dat && \
    chmod -R 777 data

# Default environment parameters
ENV APP_MODE=server
ENV PORT=8080

# Run Nginx (with dynamic port translation matching Clever Cloud), Gost, and MediaMTX services in the background and boot Clever Connect Gin server
CMD sed -i "s/listen 8080;/listen ${PORT:-8080};/g" /etc/nginx/nginx.conf && service nginx start && /usr/local/bin/gost -L socks5://127.0.0.1:10805 & /usr/local/bin/mediamtx /etc/mediamtx.yml & export PORT=3000 && exec ./bin/clever-connect
