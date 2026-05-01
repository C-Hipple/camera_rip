# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Camera Rip is a web-based photo import and selection tool for Canon and Olympus cameras. It imports photos from SD cards, lets users review/select the best shots with keyboard shortcuts, and exports JPEGs and raw files (CR3, ORF) to organized directories. The final product is a single self-contained Go binary with the React frontend embedded.

## Build Commands

```bash
make install          # Install npm + Go dependencies
make build            # Build frontend → copy to backend → compile Go binary
make build-and-run    # Build everything and start server
make run              # Run already-compiled binary
make clean            # Remove build artifacts
```

**Dev mode (two terminals):**
```bash
make dev-backend      # Go server on :5001 with -dev flag (skips serving React)
make dev-frontend     # React dev server on :3000 (proxies API to :5001)
```

**CI checks (enforced in GitHub Actions):**
```bash
cd backend-go && gofmt -l .    # Go formatting
cd backend-go && go vet ./...  # Go static analysis
cd frontend && npx react-scripts test --watchAll=false  # Frontend tests
```

## Architecture

**Two main source files contain nearly all the logic:**

- `backend-go/main.go` (~1200 lines) — All HTTP handlers, file operations, thumbnail generation, device detection
- `frontend/src/App.js` (~900 lines) — Root React component with all state management and API calls

**Supporting frontend files:**
- `frontend/src/PhotoViewer.js` — Image display with zoom/pan
- `frontend/src/ConfirmModal.js` — Reusable confirmation dialog

**Build pipeline:** React build output is copied into `backend-go/frontend/` and embedded into the Go binary via `//go:embed all:frontend/build`. The Makefile orchestrates this.

## Key Backend Concepts

- **Photo storage:** `~/Pictures/photos/{timestamp-session}/` with a `selected/` subfolder and `selected/raw/` for raw files (CR3, ORF, etc.)
- **Thumbnail cache:** `~/Pictures/photos/.thumbnails/{session}/` — generated async by a 20-worker pool at 200x200px
- **Filename prefixes:** Files are prefixed with their DCIM source folder number (e.g., `100_IMG_0001.JPG` from `100CANON/` or `100OLYMP/`) to prevent collisions across multiple DCIM folders
- **Device detection:** Looks for mounted volumes at `/Volumes` (macOS) or `/media` (Linux), then scans for supported camera DCIM folders (Canon `*CANON`, Olympus `*OLYMP`)
- **Brand registry:** The `supportedBrands` table near the top of `main.go` pairs each DCIM folder suffix with its RAW extension. Add a row to support a new brand.
- **Server port:** 5001

## API Endpoints

Key routes in `main.go`: `/api/import`, `/api/photos`, `/api/save`, `/api/export-raw`, `/api/export-raw-single`, `/api/delete-imported`, `/api/delete-photos`, `/api/directories`, `/api/selected-photos`, `/api/export-status`. Photos served at `/photos/` and thumbnails at `/thumbnail/`.

## Adding Support for Other Camera Brands

Most brands can be added by appending a `cameraBrand` entry to `supportedBrands` in `backend-go/main.go` (e.g. `{suffix: "MSDCF", rawExt: ".ARW"}` for Sony). For more involved changes:
1. `findUSBMountPoint()` — device detection paths (mount point scanning)
2. `getDCIMPrefix()` — extracts the 3-digit folder prefix; adjust if your camera uses a different convention
3. `splitPrefixedFilename()` — filename parsing for the prefix
4. File extension checks — JPEG/MP4 extensions are hardcoded in the import/preview/delete handlers
