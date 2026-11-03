# Kipple

A self-hosted RSS reader for one person. It runs as a web app with a Google Reader compatible sync API in a single container.

## Prerequisites

- Docker, to run the published image or to build the image from source.
- Go 1.27 and Node 24, only to build the binary and the web app without Docker.

## Quickstart

Replace `<version>` with a tag from the [releases page](https://github.com/WPTK/Kipple/releases). `latest` follows the newest stable release.

```
docker run -d --name kipple --restart unless-stopped -p 127.0.0.1:1919:1919 -v kipple_data:/data ghcr.io/wptk/kipple:<version>
```

Open <http://127.0.0.1:1919> to create your account, or to restore a backup from another Kipple. The port is published on `127.0.0.1` only. To reach Kipple from other devices, put HTTPS in front of it with a [reverse proxy](docs/reverse-proxy.md) or use a private network. The [deploy guide](docs/deploy.md) covers the hardened run, compose files, backups, upgrades and verifying the image signature.

To build the image from source, clone the repository and build with the Dockerfile:

```
git clone https://github.com/WPTK/Kipple.git
cd Kipple
docker build -t kipple .
docker run -d --name kipple --restart unless-stopped -p 127.0.0.1:1919:1919 -v kipple_data:/data kipple
```

To build without Docker, run `npm ci && npm run build` in `web/`, then `go build ./cmd/kipple` in the repository root.

## Configuration

Most settings live in the web app. Environment variables are optional overrides, and [.env.example](.env.example) lists every one with its notes.

| Variable | Default | Description |
| --- | --- | --- |
| `KIPPLE_ADDR` | `:1919` | Address the HTTP server listens on. |
| `KIPPLE_DATA` | `/data` | Directory that holds the database and other state. |
| `KIPPLE_USERNAME` | none | User name for creating the account from the environment. Set it together with `KIPPLE_PASSWORD`. |
| `KIPPLE_PASSWORD` | none | Password for creating the account from the environment, 8 to 256 characters. |
| `KIPPLE_API_PASSWORD` | none | Initial password for sync apps, 16 to 256 characters. |
| `KIPPLE_PUBLIC_URL` | none | Address Kipple is reached at, as an absolute http(s) URL. |
| `KIPPLE_ALLOWED_HOSTS` | none | Extra host names Kipple answers to during setup and without a password. |
| `KIPPLE_TRUSTED_PROXY_IPS` | none | Addresses and ranges of your reverse proxy, trusted to report the client address. |
| `KIPPLE_ACCESS_TEAM_DOMAIN` | none | Cloudflare Access team domain. Set it together with `KIPPLE_ACCESS_AUD`. |
| `KIPPLE_ACCESS_AUD` | none | Cloudflare Access application audience tag. |
| `TZ` | none | IANA time zone that seeds the time zone setting on the first start. |
| `KIPPLE_SCHED_TICK` | `30s` | How often the scheduler looks for feeds that are due. |
| `KIPPLE_FETCH_WORKERS` | `8` | Number of concurrent fetch workers. |
| `KIPPLE_FETCH_PER_HOST` | `2` | Maximum concurrent fetches per host. |
| `KIPPLE_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn` or `error`. |
| `KIPPLE_LOG_GREADER_FORMS` | off | Set to `1` to log truncated sync form values at debug level. |

## Usage

1. Open Kipple in your browser and follow the setup steps to create your account and add feeds. You can also import an OPML file.
2. Read in the web app. Choose a layout, theme and font in Settings.
3. To sync with an app, add a Google Reader compatible account in that app:
   - Server: your Kipple address plus `/api/greader.php`
   - User name: the name you chose during setup
   - Password: an app password, separate from your web password. Create it in the last setup step or under Settings, Account & Devices, Generate new app password. Kipple shows it once.
4. Back up from Settings. Backups contain credentials, so keep them private.

Without a password, anyone who can reach Kipple has full access. Set a password before you expose it beyond your own machine.

## License

[Blue Oak Model License 1.0.0](LICENSE). Third-party licenses are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
