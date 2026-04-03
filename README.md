# get-iplayer-go

A high-performance Go rewrite of [get_iplayer](https://github.com/get-iplayer/get_iplayer) for downloading BBC iPlayer programmes. Available as both a command-line tool and a self-hosted web UI in a single binary.

## Features

- **Parallel audio & video downloads** — both streams download concurrently, with workers automatically rebalancing when one stream finishes ahead of the other
- **Automatic 1080p enhancement** — injects a true 1080p representation into streams where BBC only advertises up to 720p, with fallback if unavailable
- **Full metadata tagging** — embeds title, show, series/episode numbers, channel, genre, synopsis, broadcast date, and thumbnail artwork into the MP4
- **Human-readable filenames** — e.g. `Show.Name.S01E01.mp4`, falling back to PID if metadata is incomplete
- **Web UI** — modern single-page interface with real-time progress, cancel support, and mobile-responsive design
- **Single binary** — no runtime dependencies beyond `ffmpeg`

## Prerequisites

- Go 1.21 or later (to build from source)
- `ffmpeg` and `ffprobe`

## Installation

### Build from source

```bash
go build -o get-iplayer-go ./cmd/get-iplayer-go/
```

### Docker

Pull and run the pre-built image from the GitHub Container Registry:

```bash
docker run -p 7373:7373 -v ./downloads:/app/downloads ghcr.io/rahbut/get-iplayer-go:latest
```

Then open `http://localhost:7373` in your browser.

To build from source instead:

```bash
docker build -f deploy/Dockerfile -t get-iplayer-go .
```

## Usage

### Command line

```bash
# Download by PID
./get-iplayer-go b0123456

# Download from a URL
./get-iplayer-go https://www.bbc.co.uk/iplayer/episode/b0123456
```

No flags are required — all features are enabled by default.

Example output:

```
Resolved PID: b0123456
Searching for streams...
Using enhanced 1080p stream
Found 13 streams, selecting best quality: 1920x1080 @ 11910 kbps (dash)

Starting download to Example.Show.S01E01.mp4...

Step 1/3: Downloading Audio and Video concurrently...
  [Audio] [========================================] 100.0%
  [Video] [========================================] 100.0%

Step 2/3: Validating downloads...
  ✓ Audio validated
  ✓ Video validated

Step 3/3: Muxing to MP4...
  ✓ Mux complete!

Adding metadata tags...
All done!
```

### Web UI

```bash
./get-iplayer-go web
```

Opens a web interface at `http://localhost:7373` with real-time WebSocket progress updates, the ability to queue and cancel downloads, and step-by-step status for each.

### Docker

```bash
# Pull and run the web UI
docker run -p 7373:7373 -v ./downloads:/app/downloads ghcr.io/rahbut/get-iplayer-go:latest

# Or use the provided Compose file (pulls from GHCR by default)
docker compose -f deploy/docker-compose.yml up -d
```

See [DEPLOY.md](DEPLOY.md) for full server deployment instructions, including how to set up a persistent stack with automatic restarts.

## Performance

A typical one-hour BBC programme downloads in around 30–45 seconds on a good connection, including validation, muxing, and tagging. This is **4–6× faster** than the original Perl implementation, primarily due to concurrent stream downloading.

## Comparison with Perl version

| Feature | Perl | Go |
|---|---|---|
| Download speed | Sequential (audio then video) | **4–6× faster** (concurrent) |
| Filenames | PID only | **Human-readable** (Show.Name.SxxExx.mp4) |
| Progress | Basic percentage | **Visual progress bars** |
| Binary | Requires Perl runtime | **Single binary** |
| Metadata tagging | Optional flag | **Always on** |
| Artwork | Not embedded | **Auto-embedded** |
| Series numbers | May be incorrect | **Parsed from display titles** |
| Output | Verbose | **Clean and minimal** |
| Web UI | No | **Yes** |

## Troubleshooting

**Programme not found** — verify the PID is correct and the programme is available on iPlayer; some content is geo-restricted to the UK.

**Metadata tagging fails** — ensure `ffmpeg` is installed and on your PATH; the download will still succeed and only the tags will be missing.

**Slow downloads** — connection pooling handles most conditions; if you're on a very slow or high-latency connection you may want to increase `IdleConnTimeout` in `cmd/get-iplayer-go/dash_downloader.go`.

**Wrong series numbers** — the app parses series/episode from BBC's `display_title.subtitle` metadata field; if BBC's own metadata is incorrect the filename will reflect that.

## Legal

### TV Licence

In the United Kingdom, you must have a valid TV Licence to watch or record live TV on any channel, or to download or watch any BBC programmes on iPlayer — including catch-up content. This tool does not circumvent or alter that requirement.

- This software is intended for **personal, private use only**.
- Downloading BBC iPlayer content for redistribution or commercial purposes is not permitted.
- Please respect the [BBC Terms of Use](https://www.bbc.co.uk/usingthebbc/terms/) and the [BBC iPlayer Terms of Use](https://www.bbc.co.uk/iplayer/help/terms-and-conditions) at all times.
- The developers of this software take no responsibility for any misuse.

### Geographical Restrictions

BBC iPlayer is available to users in the United Kingdom. Some content may be geo-restricted. Attempting to circumvent geographical access restrictions may violate the BBC's terms of service.

## License

This project is released under the [GNU General Public License v3.0](LICENSE).

All code is written from scratch in Go, but this project was developed with reference to the original get_iplayer Perl application — in particular the BBC iPlayer API interactions, stream selection logic, and metadata handling. Out of respect for that work, and in keeping with the spirit of the original licence, this project adopts GPLv3.

## Credits

This project was built with reference to the original **get_iplayer** Perl application, created by Phil Lewis and maintained by the get_iplayer contributors.

- Original project: [https://github.com/get-iplayer/get_iplayer](https://github.com/get-iplayer/get_iplayer)
- Original author: Phil Lewis
- Original copyright: Copyright (C) 2008-2010 Phil Lewis, 2010- get_iplayer contributors
- Original licence: GPLv3

## Code structure

```
get-iplayer-go/
├── README.md
├── DEPLOY.md
├── LICENSE
├── go.mod
├── go.sum
├── cmd/
│   └── get-iplayer-go/
│       ├── main.go               # Entry point, CLI and web server
│       ├── pid_resolver.go       # PID extraction from URLs and strings
│       ├── stream_finder.go      # BBC API, stream discovery, 1080p synthesis
│       ├── dash_downloader.go    # DASH/MPD parsing, concurrent segment downloads
│       ├── downloader.go         # Download orchestration, validation, muxing
│       ├── download_manager.go   # Web UI download manager with WebSocket support
│       ├── websocket_handler.go  # WebSocket connection management
│       ├── progress.go           # ffmpeg output parsing, stall detection
│       ├── tagger.go             # MP4 metadata and thumbnail embedding
│       └── templates/
│           └── index.html        # Single-page web interface
└── deploy/
    ├── Dockerfile
    ├── docker-compose.yml
    └── deploy.sh                 # Build and push to registry
```
