# systemd

Starts at boot, restarts if it dies.

The unit assumes `/srv/ask-about`. Change `WorkingDirectory` and `ExecStart` together
if you put it elsewhere — the paths in `config.toml` are relative and resolve
from there.

```bash
sudo useradd --system --home-dir /srv/ask-about --shell /usr/sbin/nologin ask-about
sudo mkdir -p /srv/ask-about/data

sudo cp ask-about-linux-amd64 /srv/ask-about/ask-about
sudo cp config.toml     /srv/ask-about/config.toml
sudo cp -r docs private prompts /srv/ask-about/

sudo chown -R ask-about:ask-about /srv/ask-about
sudo chmod 600 /srv/ask-about/config.toml   # it holds the API key
sudo chmod 700 /srv/ask-about/private       # it holds a real person's history

sudo cp deploy/ask-about.service /etc/systemd/system/ask-about.service
sudo systemctl enable --now ask-about
```

Beside the binary it needs `config.toml` and whatever that points at. For a
real deployment that is `private/`, where your own corpus and photo live, and
`docs/`, which holds the default avatar image every example points at and the
sample corpora. `prompts/` is optional: both personas are compiled in, and a
missing one falls back to that copy. A missing corpus falls back the same way
— and that corpus is a fictional person, so the site will come up answering as
someone who does not exist, with a warning in the journal. A missing avatar
image is silent: the page simply shows no picture.

```bash
systemctl status ask-about
journalctl -u ask-about -f
```

After building a new binary: copy it over and `sudo systemctl restart ask-about`.
In-flight answers get ten seconds to finish first.

`/srv/ask-about/data` is the only path the service may write. If `storage.path`
points somewhere else, add it to `ReadWritePaths` or it will fail to open the
database.

# nginx

ask-about owns `/ask-about` and nothing else — page, assets, admin, invite links. The rest
of the domain is free for whatever else lives there.

The page is at `https://your-domain/ask-about/`. Invite links look like
`https://your-domain/ask-about/i/acme-corp/exampletoken2345`.

## Install

```bash
sudo cp deploy/ask-about_http.conf      /etc/nginx/conf.d/ask-about_http.conf
sudo cp deploy/ask-about_locations.conf /etc/nginx/ask-about_locations.conf
```

Set the upstream port in `ask-about_http.conf`, then add one line inside the `server`
block that should serve ask-about. In `config.toml`, `server.trusted_proxy`
must name the machine nginx runs on — the examples ship with `127.0.0.1`,
which is right for this layout — or every visitor will appear to be nginx:

```nginx
include /etc/nginx/ask-about_locations.conf;
```

```bash
sudo nginx -t && sudo systemctl reload nginx
```

Two files because nginx won't take one: `upstream` and `limit_req_zone` are
only legal at `http` level, and a `location` only inside a `server`.

Same two steps whether ask-about shares a domain or gets its own. For its own, let
certbot make the server block — `certbot --nginx -d ask-about.example.com` writes one
with working TLS — then add the include to it. There's no server block here to
copy, because certbot's will be more current than anything checked in.

Nothing in `ask-about_locations.conf` is set outside `/ask-about` and `/ask-about/`, so adding
it to a site that already exists leaves the rest of that site alone.

## Check it

```bash
# Sentences should trickle in, not arrive together at the end.
curl -N -X POST https://your-domain/ask-about/chat \
  -H 'Content-Type: application/json' \
  -d '{"messages":[{"role":"user","text":"What was their most recent title?"}]}'

# Must say https. If it says http, X-Forwarded-Proto isn't arriving and link
# previews lose their image.
curl -s https://your-domain/ask-about/ | grep 'og:url'

# Expect: content-encoding: gzip
curl -sH 'Accept-Encoding: gzip' -D - -o /dev/null https://your-domain/ask-about/app.js \
  | grep -i content-encoding
```

