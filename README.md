# browserd

Headless Chromium packaged with a small Go proxy so you always have a stable `browserWSEndpoint` that Remote Puppeteer clients can consume. The proxy keeps track of the internal DevTools WebSocket ID and exposes it directly on `ws://0.0.0.0:9223`, so Puppeteer (or any CDP client) can connect immediately—no extra HTTP round-trips.

## Quick start

```bash
# Download the seccomp profile (store it next to your Dockerfile)
curl -o chromium.json https://raw.githubusercontent.com/peedief/browserd/main/chromium.json

# Run it and publish the proxy port with Chromium's seccomp profile
docker run --rm \
  --security-opt seccomp=chromium.json \
  -p 9223:9223 --name browserd \
  ghcr.io/peedief/browserd:v1.0.0
```

Once the container is up, the DevTools endpoint is ready at `ws://localhost:9223`. You can point Puppeteer straight at that URL; the proxy handles discovering the internal `/devtools/browser/<id>` behind the scenes and keeps the endpoint stable, which means you can safely run multiple containers behind a load balancer or network proxy.

## Using with Puppeteer `connect`

Just point Puppeteer to the proxy’s socket URL (replace `localhost` with your host/IP as needed).

```ts
import puppeteer from 'puppeteer-core';

async function main() {
  const browser = await puppeteer.connect({
    browserWSEndpoint: 'ws://localhost:9223',
  });

  try {
    const page = await browser.newPage();
    await page.goto('https://example.com');
    console.log(await page.title());
    await page.close();
  } finally {
    browser.disconnect(); // Don't browser.close()
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
```

Because the proxy maintains a single connection to Chromium’s DevTools backend, you do not need to know the randomly generated `/devtools/browser/<id>` ahead of time—connecting to `ws://<host>:9223` is all it takes. The `/healthz` endpoint is only there for debugging/monitoring.

## Docker Compose

```yaml
services:
  browserd:
    image: ghcr.io/peedief/browserd:v1.0.0
    ports:
      - "9223:9223"
    security_opt:
      - seccomp=chromium.json
```

```bash
# Place chromium.json next to docker-compose.yml, then:
curl -o chromium.json https://raw.githubusercontent.com/peedief/browserd/main/chromium.json
docker compose up -d browserd
```

This example mirrors the quick-start flow—Chromium runs under the provided seccomp profile and exposes `ws://localhost:9223` for Puppeteer clients. Add further options (env vars, volumes, etc.) as needed for your setup.
