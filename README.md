# Go Path-Based Reverse Proxy

Forward paths on your public domain to local ports.

## Setup

```bash
go mod tidy
go run main.go
```

Or build a binary:

```bash
go build -o proxy .
./proxy -config config.yaml
```

## How it works

- `domain.com/e1/*`  →  `localhost:9010` (prefix stripped)
- `domain.com/e2/*`  →  `localhost:8010` (prefix stripped)
- `domain.com/healthz`  →  returns `ok`

The path prefix is stripped before forwarding, so `/e1/api/users`
hits your backend as `/api/users`.

## Adding routes

Edit `config.yaml`:

```yaml
listen: ":80"

routes:
  - prefix: /e1
    backend: http://localhost:9010
  - prefix: /e2
    backend: http://localhost:8010
  - prefix: /dashboard
    backend: http://localhost:5173
```

Restart the binary to pick up changes.

## TLS / HTTPS

To serve HTTPS, replace `http.ListenAndServe` with `http.ListenAndServeTLS`
in main.go and point it at your cert and key files:

```go
http.ListenAndServeTLS(cfg.Listen, "cert.pem", "key.pem", mux)
```

Or put Caddy / Nginx in front and let this proxy run on a local port.

## Running as a systemd service

```ini
[Unit]
Description=Go reverse proxy
After=network.target

[Service]
ExecStart=/opt/proxy/proxy -config /opt/proxy/config.yaml
Restart=always

[Install]
WantedBy=multi-user.target
```
