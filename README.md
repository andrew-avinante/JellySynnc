# JellySynnc

Sync media from remote Jellyfin servers to a local Jellyfin instance using `.strm` files. When a remote has media your local server doesn't, JellySynnc writes a `.strm` pointer file so Jellyfin can stream it directly from the remote: no copying, no duplication.

## How it works

1. Scans your local (target) Jellyfin library to build a coverage map of what's already present
2. Scans each configured remote Jellyfin library for media
3. For each item on a remote that isn't already on the target, writes a `.strm` file containing the direct stream URL
4. For each previously synced item that has since been removed from its remote, deletes the `.strm` file

Media is matched by provider IDs (IMDB, TVDB, TMDB, etc.), so duplicates across remotes are deduplicated. When the same title exists at multiple resolutions (e.g., 1080p on one remote, 4K on another), JellySynnc tracks them separately and appends a resolution suffix to the filename.

## Features

- **No file copying**: `.strm` files are tiny text files; Jellyfin streams the actual content from the remote
- **Multi-remote**: sync from any number of remote Jellyfin servers
- **Deduplication**: provider ID matching prevents writing the same item twice
- **Multi-resolution support**: 1080p and 4K versions of the same title coexist with automatic filename suffixes
- **Tie-breaking**: configurable priority rules when the same item is available from multiple remotes (prefer `h265`, prefer `4K`, etc.)
- **Incremental sync**: tracks synced items in SQLite; only writes what's missing, only removes what's gone
- **Safe deletion**: refuses to delete any file that isn't a `.strm`
- **Two run modes**: one-shot (`sync`) or daemon with polling (`listen`)
- **Structured logging**: `log/slog` JSON-friendly output throughout
- **No CGO**: pure-Go SQLite via `modernc.org/sqlite`

## Installation

### From source

Requires Go 1.21+.

```bash
go install github.com/andrew-avinante/JellySynnc@latest
```

### Build manually

```bash
git clone https://github.com/andrew-avinante/JellySynnc
cd JellySynnc
go build -o JellySynnc .
```

## Usage

```
JellySynnc --config config.yaml sync     # one-shot sync
JellySynnc --config config.yaml listen   # sync on start, then poll on interval
```

### Flags

| Flag | Description |
|---|---|
| `--config` | Path to config YAML (required) |

## Configuration

JellySynnc is configured via a YAML file. All fields can also be set via environment variables prefixed with `JellySynnc_` (e.g. `JellySynnc_DB_PATH`).

### Example `config.yaml`

```yaml
# Local Jellyfin server that will receive .strm files
target:
  name: "Home"
  url: "http://localhost:8096"
  api_key: "your-local-api-key"

# SQLite database path (default: JellySynnc.db)
db_path: /var/lib/JellySynnc/JellySynnc.db

# How often to poll when running in listen mode (default: 15m)
poll_interval: 30m

# Remote Jellyfin servers to sync from
remotes:
  - id: "remote-a"
    name: "Friend's Server"
    api_url: "https://jellyfin.friend.example.com"
    api_key: "remote-api-key"

    # Base URL written inside .strm files. Must be reachable from the
    # machine running Jellyfin (can differ from api_url).
    strm_url: "https://jellyfin.friend.example.com"

    # Filesystem path prefix on the remote where media files live.
    # JellySynnc strips this when building relative paths.
    root_start: "/media"

    # Map each remote library to a local directory where .strm files will be written
    library_mappings:
      - remote_name: "Movies"
        local_path: "/media/strm/Movies"
      - remote_name: "TV Shows"
        local_path: "/media/strm/TV Shows"
        # Sync items that have no IMDB/TVDB/TMDB IDs (default: false)
        sync_unknown_provider_ids: true

  - id: "remote-b"
    name: "Second Server"
    api_url: "http://192.168.1.50:8096"
    api_key: "another-api-key"
    strm_url: "http://192.168.1.50:8096"
    root_start: "/srv/media"
    library_mappings:
      - remote_name: "Movies"
        local_path: "/media/strm/Movies"

# When the same item is available from multiple remotes, these rules pick the winner.
# Rules are evaluated in order; first match wins. Falls back to config order.
tie_breaker_fields:
  - field: "encoding"
    value: "h265"
  - field: "resolution"
    value: "4K"
```

### Config reference

| Key | Type | Default | Description |
|---|---|---|---|
| `target.url` | string | required | Local Jellyfin base URL |
| `target.api_key` | string | required | Local Jellyfin API key |
| `db_path` | string | `JellySynnc.db` | SQLite database file path |
| `poll_interval` | duration | `15m` | Polling interval for `listen` mode |
| `remotes[].id` | string | required | Unique identifier for this remote |
| `remotes[].api_url` | string | required | Jellyfin API endpoint for the remote |
| `remotes[].strm_url` | string | required | Base URL written inside `.strm` files |
| `remotes[].api_key` | string | required | API key for the remote |
| `remotes[].root_start` | string | required | Filesystem prefix to strip from remote paths |
| `remotes[].library_mappings` | list | required | Maps remote library names to local `.strm` directories |
| `remotes[].library_mappings[].sync_unknown_provider_ids` | bool | `false` | Sync items with no provider IDs (IMDB/TVDB/TMDB); uses a synthetic `jellyfin:<remote_id>:<item_id>` key for deduplication |
| `tie_breaker_fields` | list | none | Ordered preference rules for multi-remote deduplication |

### Getting a Jellyfin API key

Dashboard > Administration > API Keys > + (add key)

## Running as a systemd service

Create `/etc/systemd/system/JellySynnc.service`:

```ini
[Unit]
Description=JellySynnc Jellyfin sync daemon
After=network.target

[Service]
Type=simple
User=jellyfin
ExecStart=/usr/local/bin/JellySynnc --config /etc/JellySynnc/config.yaml listen
Restart=on-failure
RestartSec=10s

# Optional: write structured logs to journald
StandardOutput=journal
StandardError=journal
SyslogIdentifier=JellySynnc

[Install]
WantedBy=multi-user.target
```

Then enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now JellySynnc
sudo journalctl -u JellySynnc -f
```

## How `.strm` files work

A `.strm` file is a plain text file containing a single URL. Jellyfin treats it like any other media file; it reads the URL and streams the content directly. No transcoding happens on the JellySynnc side.

Example `.strm` content:
```
https://jellyfin.friend.example.com/Videos/abc123/stream?api_key=...
```

The file is placed in the local path you configure under `library_mappings`, preserving the original directory structure from the remote (minus the library root).

## Planned features

- **Webhook mode**: receive Jellyfin webhook events (`ItemAdded`, `ItemDeleted`) to sync in real time rather than on a polling interval — eliminates the lag between a remote adding content and the `.strm` file appearing locally
- **Post-sync library scan**: after writing or deleting `.strm` files, trigger a Jellyfin library scan on the affected local libraries via the API so new content appears immediately without waiting for Jellyfin's scheduled scan
- **Dry-run mode**: preview what would be added or removed — paths, URLs, affected remotes — without writing any files or touching the database; useful for validating config before a first run
- **Push notifications**: emit a summary on sync completion or error to ntfy, Discord, or Slack
- **Selective sync filters**: per-remote or global rules to include or exclude media by type, year range, genre, or title pattern

## Database

JellySynnc keeps a SQLite database (`JellySynnc.db` by default) with two tables:

- **`synced_items`**: one row per `.strm` file written, tracking remote ID, Jellyfin item ID, provider IDs, resolution, encoding, and file path
- **`sync_runs`**: one row per sync execution with start time, status, items added/removed, and any error message

The database is the source of truth for the removal pass; JellySynnc never scans the filesystem for orphans.
