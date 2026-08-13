# viewit

> Every server file, right in your browser.

View files on your server directly in the browser: browse tar/zip without extracting, scroll huge JSONL/logs, preview images and video instantly. One binary, zero dependencies, up and running with a single command.

Images, video, audio, source code, Markdown, PDF... open and preview right in the browser; fuzzy search finds files in huge trees in milliseconds.

## Quick Start

```bash
./build.sh                # one-command build (installs frontend deps automatically)
./viewit -root <dir>      # serves on :8080; open http://localhost:8080
```

That's it. No Node, no environment setup, no external dependencies — one static binary that runs on any Linux machine, ready to drop into Docker or an intranet server. Add `-addr 0.0.0.0:8080` to share with other devices on your network.

## Why viewit?

- **Preview, don't download** — browse files without downloading them first: images, video, audio, PDF, source code, and Markdown all render in the browser; download only when you actually need the file.
- **Two-pane Markdown preview** — source on the left, live rendering on the right, with LaTeX (KaTeX), Mermaid diagrams, code highlighting, and GFM tables/task lists; relative links navigate in place. A complete experience for writing, sharing, and reviewing documents.
- **Millisecond file search** — `Ctrl+P` or tap `Shift` three times; results appear as you type, with Unicode (Chinese, etc.) filenames supported and matches highlighted. Scopes to the current directory automatically, capped at 200 results with bounded memory.
- **Safe, private, read-only** — strict path validation and symlink protection keep every request inside the root; read-only access, and data never leaves your machine.
- **Browse archives without extracting** — zip/tar open as virtual directory trees right in the page; inner files (Markdown, code, images) preview as usual. Tar indexes are cached process-wide so re-browsing never rescans; large members are streamed on demand, no disk extraction needed.
- **Huge files, huge directories** — text/code/JSONL over 5 MB automatically switch to a streaming viewer: on-demand fetch over WebSocket with windowed rendering, memory constant regardless of file size — scroll through multi-GB logs smoothly. Huge directories render virtually too, loading one page at a time.
- **Lightweight and fast** — assets pre-compressed at build time, cache-friendly, no runtime compression; happy to run long-term on low-end hardware.

## Supported File Types

| Type | Notes |
|---|---|
| Image / video / audio | Played and previewed natively |
| Source code / text | Syntax highlighting incl. special filenames (Dockerfile, go.mod); >5 MB prompts to download |
| Markdown | Two-pane live preview: LaTeX, Mermaid, GFM, raw HTML |
| PDF / XML / JSONL | View directly |
| tar / zip | Browse inside without extracting; inner files preview as usual |

Wrong auto-detection? Click the type badge on the preview page to override per file (e.g. open an extensionless Markdown as Markdown, or highlight an unknown-language script in the language you choose); the override applies only to the current file and reverts automatically.

## For Developers

```bash
# Dev mode (frontend HMR)
go run . -dev -root <dir> -addr :8080    # backend works standalone
cd frontend && npm run dev                # Vite HMR, proxies /api to :8080

# Build & test
./build.sh      # frontend build + pre-gzip + static stripped binary -> single-file viewit
go vet ./... && go test ./...
```

- Single binary: frontend embedded via `go:embed`, pre-gzipped at build time and served with `Content-Encoding: gzip` — no per-request compression cost.
- Access logs go to stderr, three greppable categories: `[access]` per request, `[ws]` per WebSocket message, `[slow]` for slow operations (threshold tunable via `-slow`).

## License

[MIT](LICENSE).
