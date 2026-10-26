# commonplace

**A personal, self-hosted home for your e-reader highlights: import them, search them by words and by meaning, and get a daily shuffle of past highlights back.**

Highlights on a Kindle pile up in `My Clippings.txt` and are never seen again.
`cmpl` imports them into a local SQLite database, keeps the final version of
every highlight you extended or trimmed, and serves a small keyboard-driven web
app for reading them: a daily shuffle that favors what you haven't seen in a
while, search that finds passages by idea as well as by word, and dated notes
on any quote.

No accounts, no sync service, no spaced repetition. One binary and one database
file.

## Install

```
brew install --cask joshgummersall/commonplace/commonplace
```

Or from source, with [mise](https://mise.jdx.dev/):

```
mise run install   # builds bin/cmpl and copies it to ~/.local/bin
```

## Requirements

- [Ollama](https://ollama.com/) with the embedding model, for search by meaning
  and similar highlights, and a chat model to name suggested tags:

  ```
  ollama pull qwen3-embedding:0.6b
  ollama pull qwen2.5:14b
  ```

  Ollama is optional. Without it, everything works and search falls back to
  keyword matching.

## Use

Import from a Kindle (plugged in over USB) or a CrossPoint reader (Xteink X4):

```
cmpl import "/Volumes/Kindle/documents/My Clippings.txt"
cmpl import ~/Downloads/"My Clippings.txt"     # format is detected
```

Re-importing is safe. Highlights already stored are skipped, an edited
highlight replaces its older version (keeping your notes and favorite), and
bookmarks are ignored. New highlights are embedded for search when Ollama is
running; otherwise `cmpl serve` embeds them later.

Then read:

```
cmpl serve          # http://127.0.0.1:8420
```

- **Daily** shows about ten highlights, chosen once per day. Ones you haven't
  seen for longest come up first; favorites come up more often; no two make
  nearly the same point.
- **Search** matches your words and your meaning together and says why each
  result is listed. Filter by book, tag, or favorites.
- **Tags** are yours to set, rename, merge, and delete. The app suggests them
  too: a library with no tags can have its highlights grouped by meaning and
  each group named by the local model, and after that each new highlight is
  suggested the tags most of its nearest neighbors carry. Suggestions look
  muted until you confirm them; a tag you remove is never suggested again
  for that quote.
- **A quote's page** holds its notes (from the device and your own, editable)
  and a *More like this* panel of related highlights, other books first.
- **Bury** hides a quote from Daily; it stays in Search.

The app has no login. It listens on localhost; only use `--host 0.0.0.0` on a
network you trust.

### From the command line

The same library is readable without the web app, by you or by an agent
such as Claude Code. Each command prints JSON, and `cmpl help <command>`
documents its flags and output.

```
cmpl daily                         # today's daily highlights
cmpl list --favorites              # newest first; --book, --tag, --limit, --offset
cmpl search 'fear of failure'      # words and meaning, as in the web app
cmpl show 412                      # one highlight in full, with notes
cmpl similar 412                   # highlights close in meaning
cmpl books                         # book ids for --book
cmpl tags                          # tag ids for --tag
```

These commands don't change highlights, notes, tags, favorites, or burials, and
`cmpl daily` doesn't count as reading the day's set.

### With Claude Code

This repository is also a [Claude Code](https://claude.com/claude-code)
plugin marketplace. Install its plugin from inside Claude Code:

```
/plugin marketplace add joshgummersall/commonplace
/plugin install commonplace@commonplace
```

The plugin's skills read your library through `cmpl`, so `cmpl` must be on
your `PATH`. The random topic pick also needs `jq`.

- **`/commonplace:think [topic]`** tests your thinking on a topic against
  your highlights and notes. It maps the claims and tensions in what you
  highlighted, then questions you one point at a time, pressing on vague
  or inconsistent answers, until you hold a position specific enough to be
  wrong. It recommends reading aimed at the weak points and writes a
  position note where your instructions say you keep notes. With no topic,
  it picks a random tag.

### Keys

The web app is built for the keyboard, with vim-style keys. Press `?` in the
app for the full list.

| Keys | Action |
| --- | --- |
| `j` `k` (with counts, `5j`), `gg` `G`, `Ctrl-d` `Ctrl-u` | Move between quotes or notes |
| `o` `Enter` / `h` `Ctrl-o` | Open quote / back |
| `/` `gs`, `gd`, `gt`, `gf` | Search, go to Daily, go to Tags, toggle the Favorites filter |
| `t`, `T` | Edit the quote's tags (matches are listed as you type; `Tab` picks one), confirm its suggested tags |
| `f`, `b`, `s` | Favorite, bury, more like this |
| `i`, `cc`, `dd` | Write, edit, delete a note (on the Tags page: rename, delete a tag) |
| `m` | On the Tags page, merge a tag into another |
| `yy`, `u` | Copy quote with source, undo |

## Configuration

| Setting | Flag | Environment | Default |
| --- | --- | --- | --- |
| Database | `--db` | `COMMONPLACE_DB` | `$XDG_DATA_HOME/commonplace/commonplace.db`, else `~/.local/share/commonplace/commonplace.db` |
| Ollama address | `--ollama-url` | `OLLAMA_HOST` | `http://localhost:11434` |
| Embedding model | `--embed-model` | | `qwen3-embedding:0.6b` |
| Tag-naming model | `--name-model` (serve) | | `qwen2.5:14b` |

Changing the embedding model re-embeds every highlight in the background the
next time `cmpl serve` runs.

Versions before 0.4.0 kept the database in `~/.commonplace`. cmpl still uses
it there, with a note, until you stop cmpl and move `commonplace.db` (and any
`-wal` and `-shm` files) to the new default.

## Backups

```
cmpl backup ~/Library/Mobile\ Documents/com~apple~CloudDocs/commonplace --keep 30
```

writes a snapshot named by the time (`commonplace-20261025T153000Z.db`) and,
with `--keep`, removes all but the newest snapshots in that folder. It is safe
while `cmpl serve` runs. A snapshot is one file, so it is safe in iCloud Drive
or another synced folder; the live database is not, because SQLite keeps it in
several files that must change together. Run it on a schedule (cron, launchd)
for regular backups. To restore, stop cmpl and copy a snapshot to the database
path.

## Measuring search

`cmpl eval` (not listed in `--help`) scores keyword, semantic, and hybrid search
against queries you write, reporting recall@k and MRR for each, so model and
setting changes can be compared by numbers. Put the queries next to the
database in `eval.yaml`:

```yaml
k: 10
queries:
  - query: luck versus skill
    expect: [412, 977]   # highlight ids; a quote page's URL (/h/412) shows its id
```

```
cmpl eval -v
```

## Development

```
mise run dev     # serve with live reload; COMMONPLACE_DB=... for a scratch database
mise run test
mise run build
```

Templates and CSS reload on every request; Go and SQL changes rebuild and
restart the server. Schema changes go in a new
`internal/store/migrations/NNNN_description.sql`.
