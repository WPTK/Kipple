# The command line and the web app

Kipple has a handful of command-line subcommands. Everything a person does day to day has a place in the web app, and
the command line is for scripted deploys and recovery. The commands run inside the container; the form is
`docker exec kipple /kipple <command>` on the running container, or
`docker compose run --rm -T --no-deps kipple <command>` on a stopped service (see [deploy.md](deploy.md)).

| Command | What it does | In the web app |
|---|---|---|
| `kipple serve` | Runs the server. It is the container's entry point. | Not a feature: the container starts it. |
| `kipple import` | Imports an OPML file, with `-mark-read-older-than-days` and `-move-existing`. | Feeds, **Import feeds from a file (OPML)**, with the same two choices. |
| `kipple api-password` | Prints a new Reader API password once. | Settings, Account & Devices, **Generate new app password**. |
| `kipple password` | Sets the web password from the command line. | Settings, Account & Devices, **Set web password** (or **Change web password**), while you can sign in. **No web path for recovery, on purpose.** |
| `kipple restore` | Restores a backup zip or snapshot over the library. | The setup wizard restores on a new server; Settings, Account & Devices, **Reset Kipple** returns to the wizard on an existing one. See [restore.md](restore.md). |
| `kipple healthcheck` | Exits 0 when the server answers; the container's health check runs it. | Not a feature. |
| `kipple version` | Prints the version; `-v` adds the schema number and build details. | Settings, About. |

## Why password recovery stays on the command line

`kipple password` is the way back in when the web password is lost. A web page that did the same would let anyone who
can reach the address take the account over, and Kipple is often published behind a tunnel or proxy, so recovery needs
access to the machine that runs it. Changing a password you know is a web feature; recovering one you do not is not.
See [deploy.md](deploy.md#reset-the-web-password).
