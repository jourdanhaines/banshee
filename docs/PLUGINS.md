# Banshee plugins

Banshee is extended with **plugins**: self-contained directories dropped into
`~/.config/banshee/plugins/`. There are two kinds, and they cost very different
amounts of effort:

| Type   | What it is                                      | You write        |
| ------ | ----------------------------------------------- | ---------------- |
| `url`  | "Open this repo on \<service\>" — a URL template | one JSON file    |
| `exec` | A live result source (Wi-Fi, clipboard, emoji…)  | JSON + a program |

Every plugin directory contains a `manifest.json`. The daemon scans the plugin
directory once at startup and again on `banshee reload`.

```
~/.config/banshee/plugins/
├── railway-staging/
│   └── manifest.json          # url plugin — nothing else needed
└── wifi/
    ├── manifest.json
    ├── plugin                 # exec plugin binary or script
    └── icon.svg
```

The directory name is cosmetic; the `id` field inside the manifest is the
plugin's identity.

---

## 1. `manifest.json` (schema v1)

Common fields:

| Field    | Type   | Required | Meaning                                                       |
| -------- | ------ | -------- | ------------------------------------------------------------- |
| `v`      | number | yes      | Schema version. Must be `1`.                                   |
| `id`     | string | yes      | Stable id, `[A-Za-z0-9][A-Za-z0-9._-]*`. Key used by repo configs and callbacks. |
| `name`   | string | no       | Display name. Defaults to `id`.                                |
| `icon`   | string | no       | Icon-theme name, or a path relative to the plugin directory.   |
| `accent` | string | no       | CSS color for the result badge, e.g. `#a78bfa`.                |
| `type`   | string | yes      | `"url"` or `"exec"`.                                           |
| `url`    | object | if `type` is `url`  | See below.                                        |
| `exec`   | object | if `type` is `exec` | See below.                                        |

**Icon resolution.** A name matching an icon compiled into banshee
(`internal/icons/data/*.svg` — currently `github` and `railway`, rendered
tinted with the theme accent) wins; a value containing `/` or `.` is a file
path (relative paths resolve against the plugin directory, `~` is expanded);
anything else is an icon-theme name.

```json
"icon": "github"                      → bundled SVG, accent-tinted
"icon": "network-wireless-symbolic"   → icon theme lookup
"icon": "railway.svg"                 → <plugin dir>/railway.svg
"icon": "assets/logo.png"             → <plugin dir>/assets/logo.png
"icon": "/usr/share/pixmaps/x.png"    → absolute path
```

**Forward compatibility.** Unknown keys are ignored, everywhere, always. A
manifest written for a future banshee still loads on this one as long as `v` is
`1`. A malformed manifest disables only its own plugin — the rest still load,
and the error is reported by `banshee doctor`.

---

## 2. `url` plugins (connectors)

A connector answers one question: *given a repository, what URL opens it on
some service?* Banshee ships two compiled in — **GitHub** and **Railway** — and
they are ordinary manifests, so anything they do, your plugin can do too.

### `url` object

| Field              | Type    | Default              | Meaning                                        |
| ------------------ | ------- | -------------------- | ---------------------------------------------- |
| `template`         | string  | required             | URL to build. Placeholders below.               |
| `title`            | string  | `Open {repo} on {name}` | Result title. Placeholders below.            |
| `requires_binding` | boolean | `false`              | Hide the connector for repos with no binding.   |

Placeholders, substituted in both `template` and `title`:

| Placeholder | Value                                             |
| ----------- | ------------------------------------------------- |
| `{binding}` | The repo's binding for this connector (see below) |
| `{repo}`    | Repository basename, e.g. `blacksheep`            |
| `{path}`    | Absolute repository path                          |
| `{name}`    | The manifest `name` (title only)                  |

Example — `~/.config/banshee/plugins/sentry/manifest.json`:

```json
{
  "v": 1,
  "id": "sentry",
  "name": "Sentry",
  "icon": "sentry.svg",
  "accent": "#e1567c",
  "type": "url",
  "url": {
    "template": "https://sentry.io/organizations/acme/projects/{binding}/",
    "title": "Open {repo} in Sentry",
    "requires_binding": true
  }
}
```

### Binding a connector to a repository

A repository opts in through `<repo>/.banshee/config.json`:

```json
{
  "v": 1,
  "connectors": {
    "sentry": "blacksheep-api",
    "railway": "0f1c2d3e-4a5b-6c7d-8e9f-0a1b2c3d4e5f",
    "github": "https://github.com/acme/blacksheep"
  }
}
```

The value is interpreted with exactly one rule:

> **If the value parses as an absolute URL (it has a scheme and a host), it is
> used verbatim. Otherwise it is substituted into `template` as `{binding}`.**

So `"sentry": "blacksheep-api"` produces
`https://sentry.io/organizations/acme/projects/blacksheep-api/`, while
`"sentry": "https://sentry.io/whatever/i/want"` produces exactly that URL. This
is the escape hatch for repos that do not fit the template.

The file is read lazily and cached by mtime, so editing it takes effect on the
next keystroke — no reload needed. Unknown keys are ignored.

### The built-in connectors

| id        | Template                                    | Notes                                             |
| --------- | ------------------------------------------- | ------------------------------------------------- |
| `github`  | `https://github.com/{binding}`              | Falls back to the repo's git `origin` remote      |
| `railway` | `https://railway.com/project/{binding}`     | Binding is the Railway project id                 |

GitHub is the only connector with an implicit binding: for a repo that has no
`connectors.github` entry, banshee runs `git remote get-url origin` and rewrites
the result into a browsable URL, caching it against the mtime of the repo's
`.git/config`.

```
git@github.com:acme/blacksheep.git       → https://github.com/acme/blacksheep
ssh://git@github.com:22/acme/blacksheep  → https://github.com/acme/blacksheep
https://user:token@github.com/acme/bs.git → https://github.com/acme/bs
/home/me/scratch (no remote)             → no GitHub result
```

Repos that are not git repos, or have no `origin`, simply get no GitHub row.

Defining a plugin whose `id` is `github` or `railway` **overrides** the built-in
one in place — it keeps its position and category, but your template, title,
icon and accent win.

### Where connector results appear

All results derived from one repository share that repository's fuzzy score, so
a matched repo produces a fixed-order block:

```
blacksh ▸  Open blacksheep session       (session)
           Open blacksheep on GitHub     (github)
           Open blacksheep on Railway    (connector)
           Open blacksheep directory     (directory)
```

Connectors never appear for an empty query — they are always anchored to a repo
match.

---

## 3. `exec` plugins

An exec plugin is a **long-running child process** started by the daemon on
first use and kept alive. It speaks newline-delimited JSON: one JSON object per
line on stdin (host → plugin) and one per line on stdout (plugin → host).
stderr is ignored by the daemon and is yours to log to.

### `exec` object

| Field        | Type     | Default  | Meaning                                                        |
| ------------ | -------- | -------- | -------------------------------------------------------------- |
| `bin`        | string   | required | Executable. Contains `/` → resolved against the plugin dir; otherwise looked up on `$PATH`. |
| `args`       | string[] | `[]`     | Extra arguments.                                                |
| `prefix`     | string   | `""`     | Only queries starting with this word reach the plugin.          |
| `timeout_ms` | number   | `150`    | Soft per-query deadline.                                        |

```json
{
  "v": 1,
  "id": "wifi",
  "name": "Wi-Fi",
  "icon": "network-wireless-symbolic",
  "type": "exec",
  "exec": { "bin": "./plugin", "prefix": "wifi", "timeout_ms": 200 }
}
```

The process starts with `cwd` set to the plugin directory and these extra
environment variables:

| Variable               | Value                       |
| ---------------------- | --------------------------- |
| `BANSHEE_PLUGIN_ID`    | the manifest `id`           |
| `BANSHEE_PLUGIN_DIR`   | absolute plugin directory   |
| `BANSHEE_PLUGIN_PROTO` | protocol version (`1`)      |

### Prefix gating

With `"prefix": "wifi"`:

| User types    | Plugin receives  |
| ------------- | ---------------- |
| `wifi`        | `""`             |
| `wifi home`   | `"home"`         |
| `WiFi home`   | `"home"` (case-insensitive) |
| `wifikill`    | *(nothing)*      |
| `blacksheep`  | *(nothing)*      |

Without a prefix the plugin sees every non-empty query — use that only for
sources that can answer in single-digit milliseconds.

---

## 4. Protocol v1

### Host → plugin (stdin)

```jsonc
{"v":1,"event":"query","seq":42,"query":"home"}      // answer with results
{"v":1,"event":"activate","seq":43,"id":"ap-3"}      // a callback result was chosen
{"v":1,"event":"shutdown"}                            // exit; stdin closes after this
```

| Field   | Present on         | Meaning                                          |
| ------- | ------------------ | ------------------------------------------------ |
| `v`     | all                | Protocol version. Ignore events you do not know.  |
| `event` | all                | `query` \| `activate` \| `shutdown`               |
| `seq`   | query, activate    | Monotonic. **Echo it back.**                      |
| `query` | query              | The user's query, prefix stripped and trimmed.    |
| `id`    | activate           | The `id` of the result being activated.           |

### Plugin → host (stdout)

```jsonc
{"v":1,"seq":42,"event":"results","done":true,"results":[ … ]}
{"v":1,"seq":43,"event":"activated"}   // optional ack, never waited for
```

| Field     | Meaning                                                                    |
| --------- | -------------------------------------------------------------------------- |
| `seq`     | The query seq this answers. **Required** — mismatches are discarded.        |
| `event`   | `results` (or omitted). Any other value carries no results.                 |
| `results` | Array of result objects (below). May be empty.                             |
| `done`    | `true` on the final message for this seq. Unblocks the host immediately.   |

You may stream: send several `results` messages for one `seq` and set
`"done": true` only on the last. The host merges them in arrival order.

### Result object

| Field      | Type   | Default        | Meaning                                            |
| ---------- | ------ | -------------- | -------------------------------------------------- |
| `id`       | string | `""`           | Your id for the row; echoed back in `activate`.     |
| `title`    | string | required       | Rows without a title are dropped.                   |
| `subtitle` | string | `""`           | Dimmed second line.                                 |
| `icon`     | string | manifest icon  | Same resolution rules as the manifest `icon`.       |
| `accent`   | string | manifest accent| CSS color for this row's badge.                     |
| `score`    | number | `50`           | Rank within the plugin category. Higher is earlier. |
| `action`   | object | callback       | What activation does (below).                       |

### Action object

| `kind`          | Extra fields | Behavior                                                |
| --------------- | ------------ | ------------------------------------------------------- |
| `"url"`         | `url`        | Opened with the system handler.                          |
| `"exec-detach"` | `argv`       | Run detached from the daemon (survives it).              |
| `"clipboard"`   | `text`       | Copied to the system clipboard (wl-copy/xclip/xsel).     |
| `"callback"`    | —            | Sends an `activate` event back to your plugin.           |

`callback` is the default: a result with no `action`, an unknown `kind`, a
`url` action with no `url`, an `exec-detach` action with an empty `argv`, or a
`clipboard` action with no `text` all become callbacks — which also means a
banshee older than the `clipboard` kind degrades it to a callback instead of
erroring. The launcher hides itself before dispatching, so a callback
plugin is free to take its time.

---

## 5. Lifecycle rules

These are the rules that keep a slow or broken plugin from freezing the
launcher. Design against them.

**Lazy start.** The process is spawned on the first query that passes the prefix
gate, not at daemon startup.

**Sequence numbers.** The host tracks exactly one in-flight query. When you type
another character, the previous query is superseded and *every* message tagged
with its `seq` is thrown away. Always echo the `seq` you were given; never
answer a query you were not asked.

**Soft timeout.** `exec.timeout_ms` (default 150ms, clamped to 2000ms) is a
deadline for the *answer*, not for the process. When it expires the host returns
whatever results already arrived for that `seq` and stops listening for more.
Slow sources should stream a cheap partial answer immediately and refine it —
but be aware the refinement only lands if the user has not typed since. The
ceiling exists because the aggregator joins every provider before it paints: one
plugin's timeout is the whole launcher's.

**Cancellation.** If the user closes the launcher mid-query the host stops
waiting. The plugin is not signalled; the next `query` event simply gets a new
`seq`.

**Crash handling.** If the process exits without being asked to, the host counts
a crash and restarts it lazily after a backoff of 250ms, doubling per
consecutive crash up to 5s. **Three crashes within 30 seconds disable the plugin
until `banshee reload`.** A disabled plugin silently contributes no results.
Failing to *start* at all (missing binary, no execute bit, bad interpreter)
counts the same way, and so does emitting a line longer than 1 MiB — the host
kills a plugin it can no longer read rather than waiting on it forever.

**Shutdown.** On daemon exit or reload the host writes
`{"v":1,"event":"shutdown"}`, closes your stdin and waits 500ms; a process still
alive after that is killed. Plugins run in their own process group, so anything
you backgrounded is killed with you — do not rely on outliving the host. Exit on
`shutdown`, or on EOF from stdin. Shutdown is final: a reload builds a fresh
process, it never resurrects the old one.

**Robustness.** Lines that are not JSON objects are ignored, not fatal. Never
write anything to stdout that is not a protocol message — logs belong on stderr.
Do not buffer stdout: if your language line-buffers only for TTYs (Python,
Node), flush explicitly after every message.

---

## 6. Worked example

`plugins/example/` in the repo is a complete, commented exec plugin. Install it:

```sh
cp -r plugins/example ~/.config/banshee/plugins/example
banshee reload
```

Open the launcher and type `demo tea`. Here is the whole exchange.

**1. The manifest** declares the prefix and a 150ms budget:

```json
{
  "v": 1, "id": "example", "name": "Example",
  "icon": "utilities-terminal-symbolic", "accent": "#7aa2f7",
  "type": "exec",
  "exec": { "bin": "./plugin.sh", "prefix": "demo", "timeout_ms": 150 }
}
```

**2. You type `demo tea`.** The prefix matches, so banshee starts
`./plugin.sh` (cwd = the plugin directory) and writes one line to its stdin:

```json
{"v":1,"event":"query","seq":1,"query":"tea"}
```

Note that `demo ` has been stripped: the plugin only ever sees its own
arguments.

**3. The plugin answers** on stdout with a single line — three results, one of
each action kind, `done` set so the host does not wait out the timeout:

```json
{"v":1,"seq":1,"event":"results","done":true,"results":[
  {"id":"hello","title":"Hello from the example plugin","subtitle":"you typed: tea","score":90,"action":{"kind":"callback"}},
  {"id":"docs","title":"Read the banshee plugin docs","subtitle":"docs/PLUGINS.md","score":80,"icon":"text-x-generic-symbolic","action":{"kind":"url","url":"https://github.com/jourdanhaines/banshee/blob/main/docs/PLUGINS.md"}},
  {"id":"notify","title":"Send a test notification","subtitle":"exec-detach demo","score":70,"action":{"kind":"exec-detach","argv":["notify-send","banshee example","it works"]}}
]}
```

**4. Banshee renders three rows** in the Plugin category. The first two inherit
the manifest accent `#7aa2f7`; the first inherits the manifest icon, the second
overrides it with its own theme icon.

**5. You type another character** — `demo team`. The host sends
`{"v":1,"event":"query","seq":2,"query":"team"}`. If a straggling
`{"seq":1,…}` message arrives now it is discarded: seq 1 is no longer the
in-flight query.

**6. You press Enter on "Hello…".** Its action is a callback, so banshee hides
the window and sends:

```json
{"v":1,"event":"activate","seq":3,"id":"hello"}
```

The plugin pops a notification and acknowledges with
`{"v":1,"seq":3,"event":"activated"}`, which the host does not wait for.

Picking the second row instead would have opened the URL directly, and the
third would have run `notify-send` detached — neither involves the plugin at
all.

**7. On `banshee reload`** the host writes `{"v":1,"event":"shutdown"}`, the
plugin's `while read` loop sees the event and exits 0, and a fresh process is
started on the next `demo` query.

---

## 7. Checklist

- [ ] `manifest.json` has `v: 1`, a unique `id`, and a valid `type`
- [ ] url plugins: `template` is an absolute URL, `{binding}` where the repo-specific part goes
- [ ] url plugins: `requires_binding: true` unless the connector is meaningful for every repo
- [ ] exec plugins: every message echoes the `seq` it answers
- [ ] exec plugins: `done: true` on the final message, so the host does not wait out the timeout
- [ ] exec plugins: stdout is flushed per line and carries protocol messages only
- [ ] exec plugins: `shutdown` (and stdin EOF) exits the process
- [ ] `banshee doctor` reports no plugin errors
