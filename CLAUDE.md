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
- `frontend/src/GalleryUploadModal.js` — Title/hashtag form and album dropdown shown before a gallery upload
- `frontend/src/EditModal.js` — Crop overlay (with an optional aspect ratio lock), the white balance pair with its grey-point picker, and the exposure/black/highlights/shadows/sky sliders, with a canvas preview that runs the same tone curve as the backend

**Build pipeline:** React build output is copied into `backend-go/frontend/` and embedded into the Go binary via `//go:embed all:frontend/build`. The Makefile orchestrates this.

## Key Backend Concepts

- **Photo storage:** `~/Pictures/photos/{timestamp-session}/` with a `selected/` subfolder and `selected/raw/` for raw files (CR3, ORF, etc.)
- **Thumbnail cache:** `~/Pictures/photos/.thumbnails/{session}/` — generated async by a 20-worker pool at 200x200px
- **Filename prefixes:** Files are prefixed with their DCIM source folder number (e.g., `100_IMG_0001.JPG` from `100CANON/` or `100OLYMP/`) to prevent collisions across multiple DCIM folders
- **Device detection:** Looks for mounted volumes at `/Volumes` (macOS) or `/media` (Linux), then scans for supported camera DCIM folders (Canon `*CANON`, Olympus `*OLYMP` and `*OMSYS`)
- **SD card free space:** `/api/sd-space` statfs's the mount point `findUSBMountPoint()` reports and returns `{usb_connected, name, mount_point, total_bytes, free_bytes, used_bytes}`; `free_bytes` counts blocks available to an unprivileged writer, so used plus free can fall short of total. The `diskUsage()` syscall lives in `backend-go/diskspace_unix.go` (macOS/Linux) with a stub in `diskspace_other.go`. The sidebar draws it as a capacity meter above the delete button, and hides it when no card is mounted.
- **Brand registry:** The `supportedBrands` table near the top of `main.go` pairs each DCIM folder suffix with its RAW extension. Add a row to support a new brand.
- **Server port:** 5001
- **Photo editing:** Editing rewrites the photo in place and keeps the untouched original at `{session}/unedited/{filename}`, with the settings that produced the current render in `{session}/.edits.json`. Every render starts from the backup, so re-editing never compounds. `renderEdit()` bakes EXIF orientation into the pixels (the browser rotates the photo before the user draws a crop, so the two must agree), runs the tone pass over the whole frame, *then* crops — that order is what anchors the sky gradient to the photo rather than to the crop. The tone pass is three 256-entry LUTs per row — one per colour channel, which is what lets white balance move them against each other: white balance, exposure and the sky pull in linear light, highlights, shadows and the black point in display space, nothing clamped until the end so the highlight shoulder can recover a value the exposure lift pushed past white. `toneParams` is one channel's worth of that, and the JS mirror takes an object with the same field names. The LUT is duplicated in `frontend/src/EditModal.js` for the live preview; `TestToneLUTReferenceValues` and `EditModal.test.js` assert the same table so the two cannot drift. Edits also refresh the `selected/` copy so exports aren't stale, and drop the cached thumbnail.
- **Sky balance:** Three controls for the bird-against-bright-sky case, where the exposure needed for the bird blows the sky out. `Shadows` (0…100) is `applyShadows()` and is the one to reach for first: a curve pinned at both black and `maxShadowKnee`, so it opens the bird up and *cannot* touch anything above the knee — the sky comes through bit for bit. `Highlights` (−100…+100) is `applyHighlights()`, a soft shoulder that squeezes the open-ended range above a knee into the gap left under white, for a sky that is already clipping. `Sky` (0…100) is `applySkyPull()`, a graduated compression of up to `maxSkyPull` stops towards `skyPivot` (middle grey in linear light), positioned by `skyGradient()`, which covers the frame down to the `Horizon` and eases out over the `skyFeather` band below it — so a horizon at 100%, the default, pushes the whole feather off-frame and pulls evenly. Its promise is softer than the shadow lift's: deep shadows are exact, midtones very nearly so. A bird as pale as its sky can't be separated at all; `Highlights` alone is the tool for those. `applyTone()` rebuilds the row LUTs only when the gradient weight actually changes, so the flat regions above and below the feather cost one trio each.
- **Monotonicity:** every curve in the tone pass is monotone by construction, and `TestToneCurveIsMonotone` (mirrored in `EditModal.test.js`) sweeps the slider grid to keep it that way. It is not theoretical — the sky pull was originally a brightness-weighted multiply, and past ~1.2 stops its gain fell faster than the value rose, so inputs 150→200 came out 121→107 and a smooth sky rendered with its gradient running backwards. Compressing towards a pivot keeps every slope between `2^-stops` and 1. Any new adjustment belongs in the sweep.
- **White balance:** `Temperature` and `Tint` (both −100…+100) become per-channel linear-light gains in `whiteBalanceGains()` — temperature trades red against blue, tint moves green against the other two, `maxWhiteBalance` stops each way. The **grey-point picker** is the fast path and is frontend-only: `greyPointWhiteBalance()` in `EditModal.js` solves the two equations that make one sampled colour neutral, and the modal samples a 5×5 patch of the *unadjusted* preview frame (kept in `sourceFrameRef`) so it solves against the photo's own colours rather than the ones on screen. `TestRenderEditNeutralisesACast` runs the same arithmetic through a real render to check the answer actually lands on grey.
- **Crop aspect lock:** A frontend-only affordance — the backend still receives a plain rectangle. `normalizedAspect()` turns a preset from `ASPECT_PRESETS` into the width-to-height ratio of the crop *in frame fractions* (a w × h crop covers `w*imageWidth` by `h*imageHeight` pixels, so a pixel ratio of `pw:ph` means `w/h = (pw/ph) * (imageHeight/imageWidth)`), and presets are flipped for a portrait photo so 3:2 crops it upright. `fitCropToAspect()` reshapes an existing crop without ever growing it; `rectFromAnchor()` constrains a drag, following whichever axis the pointer reached furthest and shrinking to the room left before the frame edge. The lock deliberately outlives one photo so a whole shoot can be cropped to one shape.
- **Gallery upload:** `GALLERY_BASE_URL` and `GALLERY_PASSWORD` (read at startup by `loadGalleryConfig()`) point at a photo gallery exposing `POST /api/upload`. Both must be set or `/api/gallery-config` reports `configured: false` and the frontend hides the Upload to Gallery button. The backend does the upload so the password never reaches the browser; `normalizeTags()` canonicalises the hashtag field into the comma separated list the gallery expects.
- **Gallery albums:** `/api/gallery-albums` proxies the gallery's own `GET /api/albums` (public there, so `fetchGalleryAlbums()` sends no password) and hands the frontend `{albums: [{id, slug, title, count}]}` for the upload modal's dropdown. The list is fetched again whenever the modal opens, so an album created in the gallery mid-session shows up. The chosen album's id rides along as the upload's `album` field; a gallery that cannot be reached leaves the dropdown out rather than blocking an upload that works fine without one.

## API Endpoints

Key routes in `main.go`: `/api/import`, `/api/photos`, `/api/save`, `/api/export-raw`, `/api/export-raw-single`, `/api/delete-imported`, `/api/delete-photos`, `/api/sd-cleanup`, `/api/sd-space`, `/api/directories`, `/api/rename-directory`, `/api/selected-photos`, `/api/export-status`, `/api/gallery-config`, `/api/gallery-albums`, `/api/gallery-upload`. Editing adds `/api/edits`, `/api/edit-photo` (which takes `crop`, `temperature`, `tint`, `exposure`, `black`, `highlights`, `shadows`, `sky` and `horizon`) and `/api/revert-photo` (handlers in `edit.go`). Photos are served at `/photos/{session}/{file}` — with an optional subfolder, `/photos/{session}/unedited/{file}`, for the backed-up original — and thumbnails at `/thumbnail/`.

`/api/directories` returns one object per session — `{name, photo_count, selected_count}` — so the frontend's directory selector can label a session with how much of it survived review (e.g. `2025-12-11 Holiday Party (12 / 200)`). Both counts come from `countPhotoFiles`, which shares `listPhotoFiles` with `/api/photos` and `/api/selected-photos` so the numbers always match the lists those endpoints return. Subfolders are not counted, which is what keeps `selected/raw/` and an edited photo's `unedited/` backup out of the totals.

## Adding Support for Other Camera Brands

Most brands can be added by appending a `cameraBrand` entry to `supportedBrands` in `backend-go/main.go` (e.g. `{suffix: "MSDCF", rawExt: ".ARW"}` for Sony). For more involved changes:
1. `findUSBMountPoint()` — device detection paths (mount point scanning)
2. `getDCIMPrefix()` — extracts the 3-digit folder prefix; adjust if your camera uses a different convention
3. `splitPrefixedFilename()` — filename parsing for the prefix
4. File extension checks — JPEG/MP4 extensions are hardcoded in the import/preview/delete handlers
