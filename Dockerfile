FROM golang:1.25.4 AS builder

WORKDIR /src

COPY ./src/go.mod ./src/go.sum ./
RUN go mod download

COPY ./src .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/chromium-proxy .

FROM debian:stable-slim

ENV DEBIAN_FRONTEND=noninteractive \
    CHROMIUM_REMOTE_DEBUGGING_URL=http://127.0.0.1:9222 \
    LISTEN_ADDR=:9223

RUN apt-get update && apt-get install -y \
    chromium \
    fonts-liberation \
    libappindicator3-1 \
    libasound2 \
    libatk-bridge2.0-0 \
    libnspr4 \
    libnss3 \
    libxss1 \
    libx11-xcb1 \
    libxcomposite1 \
    libxdamage1 \
    libxrandr2 \
    libgbm1 \
    libgtk-3-0 \
    ca-certificates \
    curl \
    wget \
    chromium-sandbox \
    --no-install-recommends && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/chromium-proxy /usr/local/bin/chromium-proxy
COPY ./src/start.sh /usr/local/bin/start-chromium

RUN useradd -m chromiumuser && \
    chmod +x /usr/local/bin/start-chromium

USER chromiumuser
WORKDIR /home/chromiumuser

EXPOSE 9223

HEALTHCHECK --interval=30s --timeout=10s --start-period=15s --retries=3 \
  CMD curl -f http://127.0.0.1:9223/healthz || exit 1

CMD ["/usr/local/bin/start-chromium"]
