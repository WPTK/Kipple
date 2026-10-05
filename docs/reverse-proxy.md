# Running Kipple behind a reverse proxy

Kipple speaks plain HTTP on one port. A reverse proxy (or a tunnel such as Cloudflare Tunnel) adds HTTPS and a public
name. This page gives working snippets for Caddy, nginx and Traefik, and says exactly what Kipple believes from a
proxy, because a wrong setup does not fail loudly: it makes every visitor look like the proxy.

- [What Kipple needs from a proxy](#what-kipple-needs-from-a-proxy)
- [Find the address to trust](#find-the-address-to-trust)
- [Caddy](#caddy)
- [nginx](#nginx)
- [Traefik](#traefik)
- [Open mode and proxies](#open-mode-and-proxies)
- [Check that it works](#check-that-it-works)

## What Kipple needs from a proxy

Kipple decides who the client is in one place. A connection from an address that is **not** in the trusted proxies
(Settings, Account & Devices, Address and access) is the client itself, and every forwarding header it sends is ignored. For a connection from
a listed address:

- The client is the rightmost `X-Forwarded-For` hop that is not itself a listed proxy. Each proxy appends the address
  it received the connection from, so entries further left were written by someone the chain does not vouch for.
- With no `X-Forwarded-For`, `CF-Connecting-IP` is the client (Cloudflare Tunnel).
- `X-Forwarded-Proto: https` makes the request count as HTTPS (the `Secure` cookie flag, and the scheme in the
  same-origin check).

That client address keys the web sign-in and Reader API login budgets (see "Ports" in [deploy.md](deploy.md#ports)). So
a proxy **must**:

1. Connect from an address you list under **Trusted proxies** (a single address or a CIDR range, one per line).
2. Pass the original `Host` header through unchanged. Kipple refuses a state-changing request whose `Origin` does not
   match the host it was sent to.
3. Send `X-Forwarded-For` containing the address it received the connection from, appended after whatever the client
   sent, and `X-Forwarded-Proto` (`https` when it terminated TLS).
4. Reach Kipple only through the published port or network you intend: anything else that can connect to Kipple from a
   listed address can write its own forwarding headers and be believed.

A proxy **must not**:

- Be listed with a range wider than the proxies themselves. A range of every address (`0.0.0.0/0`) is refused.
- Rewrite `Host` to the upstream's address (`proxy_set_header Host $proxy_host` in nginx, a `Host` rewrite in a
  Caddy `header_up`, and so on).
- Strip `X-Forwarded-Proto` when it terminates TLS, or forward a client's `X-Forwarded-Proto` unchanged.
- Buffer or time out the live-update stream `/api/events`. Kipple sends a heartbeat every 15 seconds and the
  `X-Accel-Buffering: no` header, so the default timeouts of the proxies below are fine.

Requests larger than 8 MiB are refused for OPML import through the web app, so a proxy body limit of 8 MiB or more is
enough.

`/api/greader.php` is the path sync apps use. It must reach Kipple with the same rules as the rest. If an access layer
such as Cloudflare Access sits in front, that path normally bypasses it, since sync apps cannot sign in to one; Kipple's
Reader API has its own password. A sync client that runs in a browser sends an `OPTIONS` preflight there first, without
the password; Kipple answers it, so the bypass must cover `OPTIONS` as well as `GET` and `POST`, and the proxy must not
answer it or add CORS headers of its own.

Kipple compresses its responses (gzip) for clients that accept it, so the proxy need not; a proxy that compresses
leaves an already compressed response alone.

## Find the address to trust

The address to list is the one Kipple sees as the connection's peer, which in Docker is not always the proxy's own
address. The easiest way to learn it is to leave the trusted proxies empty, load the site through the proxy once, and
read the log:

    docker logs kipple 2>&1 | grep "untrusted peer"

The line is a `WARN` that names the peer, for example `"peer":"172.18.0.1:53422"`. Take the address without the port
(`172.18.0.1`) and add it under **Trusted proxies** in Settings, Account & Devices, Address and access. It applies at
once. (Kipple logs this warning at most once an hour. For a scripted first start, `KIPPLE_TRUSTED_PROXY_IPS=172.18.0.1`
in `.env` seeds the setting.) The usual cases:

- **Proxy on the host, Kipple published on `127.0.0.1:1919`.** Kipple sees the Docker bridge gateway, for example
  `172.18.0.1`. The published port is reachable from this machine only, so listing the gateway is safe.
- **Proxy in a container on the same Docker network as Kipple, Kipple not published.** Kipple sees the proxy
  container's address. List that address, or the network's subnet when it is a network only your containers join.
- **Cloudflare Tunnel (`cloudflared`).** List the address `cloudflared` connects from. Kipple reads `CF-Connecting-IP`
  from it.

Also set the **Public URL** in the same place (for example `https://rss.example.com`; the setup wizard asks for it too)
so sync clients get feed icons and the name passes the Host check.

## Caddy

    rss.example.com {
        reverse_proxy 127.0.0.1:1919
    }

`reverse_proxy` passes `Host` through, appends the client address to `X-Forwarded-For`, and sets `X-Forwarded-Proto`
and `X-Forwarded-Host`; it does not trust those headers from clients unless you configure `trusted_proxies`. Leave that
unset. With Caddy and Kipple in containers on one network, use the service name instead: `reverse_proxy kipple:1919`.

## nginx

    server {
        listen 443 ssl;
        server_name rss.example.com;
        # ssl_certificate and ssl_certificate_key as usual

        client_max_body_size 8m;

        location / {
            proxy_pass http://127.0.0.1:1919;
            proxy_http_version 1.1;
            proxy_set_header Host $http_host;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
        }
    }

`$proxy_add_x_forwarded_for` appends the connecting address to whatever the client sent, which is what Kipple expects.
`$http_host` keeps the port when you serve on a non-standard one. Do not set `X-Forwarded-For` to `$remote_addr` alone on
a proxy that sits behind another proxy: it discards the chain.

## Traefik

With Docker labels, Traefik and Kipple on the same network and Kipple not published:

    services:
      kipple:
        image: ghcr.io/wptk/kipple:<version>
        container_name: kipple
        volumes: ["kipple_data:/data"]
        networks: [proxy]
        labels:
          - traefik.enable=true
          - traefik.http.routers.kipple.rule=Host(`rss.example.com`)
          - traefik.http.routers.kipple.entrypoints=websecure
          - traefik.http.routers.kipple.tls.certresolver=letsencrypt
          - traefik.http.services.kipple.loadbalancer.server.port=1919
        # the other settings of docker-compose.pull.example.yml go here as well
    networks:
      proxy:
        external: true

Traefik passes `Host`, appends the client to `X-Forwarded-For` and sets `X-Forwarded-Proto`. Leave the entry point's
`forwardedHeaders.insecure` at its default (off) unless a proxy of your own sits in front of Traefik, in which case list
that proxy under `forwardedHeaders.trustedIPs` and add its address to Kipple's list too. Kipple sees Traefik's address
on the shared network: find it as above and add it to the trusted proxies.

## Open mode and proxies

Open mode (no password) refuses any request that came through a proxy or tunnel, and treats a connection from a listed
proxy address the same way, so it fails closed. Use a password if Kipple sits behind a proxy. The exact rule is in
[deploy.md](deploy.md#open-mode-no-password).

## Check that it works

1. Sign in through the public address. If sign-in works but changing anything answers `403`, `Host` or
   `X-Forwarded-Proto` is wrong (see [troubleshooting.md](troubleshooting.md#sign-in-works-but-actions-fail-behind-a-proxy)).
2. `docker logs kipple` shows no `untrusted peer` warning after a few requests.
3. Open `https://rss.example.com/api/greader.php/check/compatibility`: it answers `PASS`.
