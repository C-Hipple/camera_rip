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

- `backend-go/main.go` (~2500 lines) — All HTTP handlers, file operations, thumbnail generation, device detection
- `frontend/src/App.js` (~1700 lines) — Root React component with all state management and API calls

**Supporting backend files:**
- `backend-go/edit.go` — Non-destructive photo editing: the crop/exposure/black render pipeline, EXIF preservation, the `unedited/` backup and the `.edits.json` sidecar

**Supporting frontend files:**
- `frontend/src/PhotoViewer.js` — Image display with zoom/pan
- `frontend/src/ConfirmModal.js` — Reusable confirmation dialog
- `frontend/src/RenameModal.js` — Single-field prompt for renaming a session folder
- `frontend/src/GalleryUploadModal.js` — Title/hashtag form shown before a gallery upload
- `frontend/src/EditModal.js` — Crop overlay and exposure/black sliders, with a canvas preview that runs the same tone curve as the backend

**Build pipeline:** React build output is copied into `backend-go/frontend/` and embedded into the Go binary via `//go:embed all:frontend/build`. The Makefile orchestrates this.

## Key Backend Concepts

- **Photo storage:** `~/Pictures/photos/{timestamp-session}/` with a `selected/` subfolder and `selected/raw/` for raw files (CR3, ORF, etc.)
- **Thumbnail cache:** `~/Pictures/photos/.thumbnails/{session}/` — generated async by a 20-worker pool at 200x200px
- **Filename prefixes:** Files are prefixed with their DCIM source folder number (e.g., `100_IMG_0001.JPG` from `100CANON/` or `100OLYMP/`) to prevent collisions across multiple DCIM folders
- **Device detection:** Looks for mounted volumes at `/Volumes` (macOS) or `/media` (Linux), then scans for supported camera DCIM folders (Canon `*CANON`, Olympus `*OLYMP` and `*OMSYS`)
- **Brand registry:** The `supportedBrands` table near the top of `main.go` pairs each DCIM folder suffix with its RAW extension. Add a row to support a new brand.
- **Server port:** 5001
- **Photo editing:** Editing rewrites the photo in place and keeps the untouched original at `{session}/unedited/{filename}`, with the settings that produced the current render in `{session}/.edits.json`. Every render starts from the backup, so re-editing never compounds. `renderEdit()` bakes EXIF orientation into the pixels (the browser rotates the photo before the user draws a crop, so the two must agree), applies the crop, then a 256-entry tone LUT — exposure in linear light, black point in display space. The LUT is duplicated in `frontend/src/EditModal.js` for the live preview; `TestToneLUTReferenceValues` and `EditModal.test.js` assert the same table so the two cannot drift. Edits also refresh the `selected/` copy so exports aren't stale, and drop the cached thumbnail.
- **Gallery upload:** `GALLERY_BASE_URL` and `GALLERY_PASSWORD` (read at startup by `loadGalleryConfig()`) point at a photo gallery exposing `POST /api/upload`. Both must be set or `/api/gallery-config` reports `configured: false` and the frontend hides the Upload to Gallery button. The backend does the upload so the password never reaches the browser; `normalizeTags()` canonicalises the hashtag field into the comma separated list the gallery expects.

## API Endpoints

Key routes in `main.go`: `/api/import`, `/api/photos`, `/api/save`, `/api/export-raw`, `/api/export-raw-single`, `/api/delete-imported`, `/api/delete-photos`, `/api/sd-cleanup`, `/api/directories`, `/api/rename-directory`, `/api/selected-photos`, `/api/export-status`, `/api/gallery-config`, `/api/gallery-upload`. Editing adds `/api/edits`, `/api/edit-photo` and `/api/revert-photo` (handlers in `edit.go`). Photos are served at `/photos/{session}/{file}` — with an optional subfolder, `/photos/{session}/unedited/{file}`, for the backed-up original — and thumbnails at `/thumbnail/`.

## Adding Support for Other Camera Brands

Most brands can be added by appending a `cameraBrand` entry to `supportedBrands` in `backend-go/main.go` (e.g. `{suffix: "MSDCF", rawExt: ".ARW"}` for Sony). For more involved changes:
1. `findUSBMountPoint()` — device detection paths (mount point scanning)
2. `getDCIMPrefix()` — extracts the 3-digit folder prefix; adjust if your camera uses a different convention
3. `splitPrefixedFilename()` — filename parsing for the prefix
4. File extension checks — JPEG/MP4 extensions are hardcoded in the import/preview/delete handlers
