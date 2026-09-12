# ask-about

A single Go binary that answers questions about one subject from one document.
Built for a work history — more than a resume holds — but the subject can be a
product or a service instead of a person, and the corpus can be anything you
want answered from.

The UI, the corpus and the persona are compiled into the binary. Deploying is
copying one file.

Requires Go 1.26 and an API key for Anthropic, OpenAI, or anything
OpenAI-compatible.

## 1. Configure

Pick the example that matches what the assistant is about and copy it:

```bash
cp config.example-person.toml config.toml && chmod 600 config.toml
```

| Example | For |
|---|---|
| `config.example-person.toml` | a person — a work history, more than a resume holds |
| `config.example-product.toml` | a product or a service |
| `config.example-company.toml` | a company, a team, an organization |

Put your key in `api_key`. `config.toml` is gitignored; every setting is
documented in the examples, and they differ only in their first part.

The corpus that ships is a fictional sample. Make it about you — and keep your
own copy out of git. `private/` is gitignored for that:

```bash
mkdir -p private && cp docs/content.md private/content.md
```

| File | What it is |
|---|---|
| `private/content.md` | your corpus, where `[corpus] path` points. Never committed |
| `private/photo.jpg` | your photo, if you want one instead of the shipped android head. Point `[avatar] photo` at it. Never committed |
| `config.toml` `[subject]` | kind, name, pronouns, tagline, starter questions |

`docs/content.md` is the fictional sample compiled into the binary as a
fallback. Editing it in place puts a real person's history in a tracked file,
which is the one mistake this layout exists to prevent.

### A product or a company instead of a person

The product and company examples set `kind = "product"` and a single `name`,
point the corpus at `docs/product.md` or `docs/company.md` — fictional samples
to try with — and switch the synthetic presenter off so `photo` is shown
plainly as a logo. The assistant then uses a persona written for a product,
service or organization — no career, no hand-off to a person, no persuading —
and refers to it as "it" unless you say otherwise.

Run it:

```bash
make dev          # serves web/ from disk — edit CSS or JS and refresh
```

Open <http://localhost:8283/ask-about/>.

## 2. Build

```bash
make build        # -> ./ask-about
```

The binary contains `web/`, `docs/content.md` and both personas under
`prompts/`. It
needs three things beside it: `config.toml`, `private/content.md`, and whatever
`avatar.photo` points at. Without the corpus it still starts — answering as the
fictional sample, with a warning in the log.

Building for a server from a Mac:

```bash
make linux        # -> ./ask-about-linux-amd64
```

Add `GOARCH=arm64` for an arm server. Nothing uses cgo, so the result is a
static binary that runs on a machine with no libraries installed.

## 3. Deploy

See [deploy/README.md](deploy/README.md) for the systemd unit, nginx and TLS.

ask-about serves everything under `/ask-about` and nothing at the root, so it can share a
domain with something else.

## 4. Admin — issuing links

Set `username` and `password` under `[admin]`, restart, and open
`/ask-about/admin`.

Enter a name — a person, a company, a job posting, whatever you'll recognise
later — and you get a link:

```
https://your-domain/ask-about/i/acme-corp/exampletoken2345
```

Every link stays on the page, so you can copy it again later. Click one to
copy it.

The page lists what each contact asked, when, and what it cost, and the search
box finds a question or an answer by any word in it. **edit** on a link shows
its visits and lets you change its note or its expiry — including extending
one that has already run out. **revoke** stops it working immediately.

To require a link before anyone can ask, set `invite_only = true` under
`[access]`.

## 5. Advanced

`prompts/person.md` and `prompts/product.md` are the system prompts, one per
kind of subject — how answers are shaped, how long they run, what happens when
the corpus doesn't cover something. They are the files to edit if answers are
the wrong length or the wrong tone. To keep your own version out of git, copy
one to `private/` and set `[corpus] persona` to it.

They use `{{firstName}}`, `{{lastName}}` and `{{fullName}}`, plus the pronoun
placeholders `{{they}}`, `{{them}}`, `{{their}}`, `{{theirs}}` and
`{{themselves}}` — each also capitalised, as `{{They}}`, for the start of a
sentence — so they survive being pointed at a different subject.
