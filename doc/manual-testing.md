# Manual Testing Guide

This document describes how to manually exercise Camera Rip end-to-end **without a
physical camera or SD card**. It is aimed at engineers and AI agents who need to
verify the import → review → export → delete lifecycle (with a focus on the Canon
path) after making backend or frontend changes.

The trick is to simulate two things the app normally reads from the real world:

1. **A camera SD card** — the app scans OS mount points (`/media/$USER` on Linux,
   `/Volumes` on macOS) for a `DCIM/` folder containing a supported brand
   directory (e.g. `100CANON`). We create that layout on disk with fake photos.
2. **The photo library** — the app writes imported photos to `$HOME/Pictures/photos`.
   We run the server with a throwaway `HOME` so testing never touches your real
   `~/Pictures`.

> These steps were used to audit the Canon integration and to verify the live
> import-progress feature. They cover every HTTP endpoint in `backend-go/main.go`.

---

## Prerequisites

- **Go 1.22+** and **Node.js + npm** (same as building the project).
- A Unix-like shell. Commands below are written for Linux; macOS differences are
  called out inline.
- `curl` for driving the API.
- (Optional) A headless Chromium + `playwright-core` for the browser check in
  [Step 8](#step-8-verify-live-import-progress-in-a-browser-optional).

Pick a scratch working directory and export a few variables that the rest of the
guide reuses:

```bash
export WORK=/tmp/camera-rip-manual      # throwaway workspace
export FAKE_HOME=$WORK/home             # isolated HOME -> photo library lives here
export REPO=/path/to/camera_rip         # this repository

# SD card mount point the server will discover.
# Linux: /media/$USER/<label>   macOS: /Volumes/<label>
export CARD=/media/$USER/CANON_SD       # macOS: export CARD=/Volumes/CANON_SD

mkdir -p "$WORK" "$FAKE_HOME"
```

> **Linux mount detection:** the server reads `os.Getenv("USER")` and scans
> `/media/$USER/`. If `$USER` is empty in your shell (some containers), set it
> explicitly (e.g. `export USER=$(whoami)`) and place the card under
> `/media/$USER/`. You need write access to that path (`sudo mkdir` if required).

---

## Step 1 — Build the backend

For API-only testing, a dev build is enough (it skips serving the React app):

```bash
cd "$REPO/backend-go"
# CI/dev builds need a placeholder embed target if you haven't built the frontend:
mkdir -p frontend/build
[ -f frontend/build/index.html ] || echo '<!DOCTYPE html><html><body>placeholder</body></html>' > frontend/build/index.html
go build -o "$WORK/camera-rip" .
```

For the browser check in Step 8 you need the **full** binary with the embedded
frontend instead:

```bash
cd "$REPO"
make frontend
rm -rf backend-go/frontend && mkdir -p backend-go/frontend
cp -r frontend/build backend-go/frontend/build
cd backend-go && go build -o "$WORK/camera-rip-full" .
```

---

## Step 2 — Create a simulated Canon SD card

The app treats `.JPG` as viewable images and matches a RAW file (`.CR3` for
Canon) by base name. Real CR3 files are ISOBMFF containers with embedded preview
JPEGs; the app extracts the largest embedded JPEG for previews/thumbnails
(`scanExtractJPEG`). The generator below writes valid JPEGs plus **synthetic
CR3s** — an ISOBMFF-ish header, a small embedded thumbnail JPEG, a larger preview
JPEG, and trailing "sensor" noise — which is enough to exercise the preview and
export code paths. It also writes a `.MP4` and a macOS `._` resource-fork file so
the type/junk filters get tested.

Save this as `$WORK/mkfixtures/main.go`:

```go
// Generates a fake Canon SD card layout (100CANON + 101CANON) with JPGs and
// synthetic CR3 files, for manual/E2E testing without real hardware.
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
)

func makeJPEG(w, h int, seed int64) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(seed))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}

func main() {
	root := os.Args[1] // e.g. /media/$USER/CANON_SD
	for _, dir := range []string{"100CANON", "101CANON"} {
		p := filepath.Join(root, "DCIM", dir)
		if err := os.MkdirAll(p, 0755); err != nil {
			panic(err)
		}
		for i := 1; i <= 3; i++ {
			base := fmt.Sprintf("IMG_%04d", i)
			seed := int64(i)
			if dir == "101CANON" {
				seed += 100 // distinct content per folder
			}
			os.WriteFile(filepath.Join(p, base+".JPG"), makeJPEG(320, 240, seed), 0644)

			// Synthetic CR3: ftyp box + small thumb JPEG + large preview JPEG + noise.
			var cr3 bytes.Buffer
			cr3.Write([]byte{0x00, 0x00, 0x00, 0x18})
			cr3.WriteString("ftypcrx ")
			cr3.Write([]byte{0x00, 0x00, 0x00, 0x01})
			cr3.WriteString("crx isom")
			cr3.Write(makeJPEG(32, 24, seed+1000))   // thumbnail
			cr3.Write(makeJPEG(640, 480, seed+2000)) // preview (largest -> what the app extracts)
			noise := make([]byte, 8192)
			rand.New(rand.NewSource(seed + 3000)).Read(noise)
			for j := 0; j+2 < len(noise); j++ { // scrub accidental JPEG SOI markers
				if noise[j] == 0xFF && noise[j+1] == 0xD8 && noise[j+2] == 0xFF {
					noise[j] = 0x00
				}
			}
			cr3.Write(noise)
			os.WriteFile(filepath.Join(p, base+".CR3"), cr3.Bytes(), 0644)
		}
		os.WriteFile(filepath.Join(p, "MVI_0001.MP4"), []byte("fake mp4"), 0644)   // video filter
		os.WriteFile(filepath.Join(p, "._IMG_0001.JPG"), []byte("junk"), 0644)     // macOS junk filter
	}
	fmt.Println("fixtures written to", root)
}
```

Generate the card:

```bash
cd "$WORK/mkfixtures" && go mod init mkfixtures 2>/dev/null; go run . "$CARD"
ls "$CARD/DCIM/100CANON"
# -> IMG_0001.JPG IMG_0001.CR3 ... MVI_0001.MP4 ._IMG_0001.JPG
```

This yields **12 importable files** (6 JPG + 6 CR3 across two folders), plus 2
videos and 2 junk files that should be filtered out.

---

## Step 3 — Run the server against an isolated HOME

```bash
HOME="$FAKE_HOME" USER="$USER" "$WORK/camera-rip" -dev &
sleep 1
curl -s http://localhost:5001/api/directories   # -> [] (nothing imported yet)
```

The server now writes to `$FAKE_HOME/Pictures/photos`, keeping your real photo
library untouched. Stop it later with `pkill -f 'camera-rip -dev'`.

---

## Step 4 — Import lifecycle (Canon)

### 4a. Preview what would be imported

```bash
curl -s -X POST http://localhost:5001/api/import-preview \
  -H 'Content-Type: application/json' \
  -d '{"import_raws": true, "skip_duplicates": true}'
```

Expected: `files_to_import: 12`, `skipped_videos: 2`, `total_files: 14`, and a
`daily_breakdown` map. The `._` files are ignored entirely.

### 4b. Import (streams NDJSON progress)

`/api/import` streams newline-delimited JSON events (`start`, `progress`, `done`).
Use `curl -N` to see them unbuffered:

```bash
curl -sN -X POST http://localhost:5001/api/import \
  -H 'Content-Type: application/json' \
  -d '{"import_raws": true, "skip_duplicates": true}'
```

Expected stream:

```
{"total":12,"type":"start"}
{"copied":1,"total":12,"type":"progress"}
...
{"copied":12,"total":12,"type":"progress"}
{"copied":12,"message":"Successfully copied 12 new files.","new_directory":"<timestamp>","skipped_duplicates":0,"type":"done"}
```

Confirm the files landed with DCIM-prefixed names:

```bash
export DIR=$(curl -s http://localhost:5001/api/directories | tr -d '[]"' | cut -d, -f1)
ls "$FAKE_HOME/Pictures/photos/$DIR"
# -> 100_IMG_0001.JPG 100_IMG_0001.CR3 ... 101_IMG_0003.CR3
```

### 4c. List photos (JPGs preferred when present)

```bash
curl -s "http://localhost:5001/api/photos?directory=$DIR"
# -> only the 6 JPGs (RAWs are hidden when a viewable JPG exists)
```

### 4d. CR3 preview extraction and thumbnails

```bash
# Embedded preview JPEG served from the CR3 (should be the 640x480 one):
curl -s "http://localhost:5001/photos/$DIR/100_IMG_0001.CR3" -o "$WORK/preview.jpg" \
  -w "HTTP %{http_code} %{size_download}b type=%{content_type}\n"
file "$WORK/preview.jpg"     # -> JPEG image data ... 640x480

# Thumbnails (RAW thumbs are JPEG bytes served as image/jpeg):
curl -s "http://localhost:5001/thumbnail/$DIR/100_IMG_0001.CR3" -o /dev/null -w "CR3 thumb: HTTP %{http_code} %{content_type}\n"
curl -s "http://localhost:5001/thumbnail/$DIR/100_IMG_0001.JPG" -o /dev/null -w "JPG thumb: HTTP %{http_code} %{content_type}\n"
```

### 4e. Duplicate detection

Re-run the import; everything should be recognized as already-imported:

```bash
curl -sN -X POST http://localhost:5001/api/import \
  -H 'Content-Type: application/json' -d '{"import_raws": true, "skip_duplicates": true}'
# -> single done event: "All 12 files have already been imported.", copied:0, skipped_duplicates:12
```

---

## Step 5 — Save selections and export RAWs

```bash
# Save two JPEGs as "selected":
curl -s -X POST http://localhost:5001/api/save -H 'Content-Type: application/json' \
  -d "{\"directory\":\"$DIR\",\"selected_files\":[\"100_IMG_0002.JPG\",\"101_IMG_0001.JPG\"]}"

# Export status before pulling RAWs:
curl -s "http://localhost:5001/api/export-status?directory=$DIR"
# -> selected_count:2, raw_count:0, missing_count:2

# Bulk-export the matching CR3s from the card:
curl -s -X POST http://localhost:5001/api/export-raw -H 'Content-Type: application/json' \
  -d "{\"directory\":\"$DIR\"}"
# -> copied:2, skipped:0, not_found:0

ls "$FAKE_HOME/Pictures/photos/$DIR/selected/raw"   # -> 100_IMG_0002.CR3 101_IMG_0001.CR3

# Single-file export for another photo:
curl -s -X POST http://localhost:5001/api/export-raw-single -H 'Content-Type: application/json' \
  -d "{\"directory\":\"$DIR\",\"filename\":\"100_IMG_0003.JPG\"}"
# -> status:"copied"
```

**Verify prefix-aware RAW matching** (the important Canon correctness check —
`100CANON` and `101CANON` both contain `IMG_0001.CR3`, and the app must pick the
one matching the JPG's prefix):

```bash
md5sum "$FAKE_HOME/Pictures/photos/$DIR/selected/raw/101_IMG_0001.CR3" \
       "$CARD/DCIM/101CANON/IMG_0001.CR3" \
       "$CARD/DCIM/100CANON/IMG_0001.CR3"
# The exported file's hash must match 101CANON, NOT 100CANON.
```

---

## Step 6 — Deletion flows

```bash
# Delete only files already imported to the library, from the card (also removes
# the associated CR3 for each deleted JPG):
curl -s -X POST http://localhost:5001/api/delete-imported
# -> deleted:6, deleted_raw:6
ls "$CARD/DCIM/100CANON"    # -> only MVI_0001.MP4 remains

# Delete a specific photo from the library (hard drive):
curl -s -X POST http://localhost:5001/api/delete-photos -H 'Content-Type: application/json' \
  -d "{\"directory\":\"$DIR\",\"files\":[\"101_IMG_0003.JPG\"]}"
# -> deleted:1
```

---

## Step 7 — Security regression: path traversal must be rejected

Every handler that accepts a `directory` (or `filename`) must keep the resolved
path inside the photo library. Create a sensitive file outside the library and
confirm it **cannot** be read or deleted via `..` traversal:

```bash
mkdir -p "$FAKE_HOME/Documents" && echo "secret" > "$FAKE_HOME/Documents/taxes.txt"

# Arbitrary-file deletion attempt — must return HTTP 400 and NOT delete the file:
curl -s -X POST http://localhost:5001/api/delete-photos -H 'Content-Type: application/json' \
  -d '{"directory":"../../Documents","files":["taxes.txt"]}' -w "  [HTTP %{http_code}]\n"
ls "$FAKE_HOME/Documents/"    # taxes.txt must STILL be present

# Directory listing / status traversal — must return HTTP 400:
curl -s "http://localhost:5001/api/photos?directory=../../Documents" -w "  [HTTP %{http_code}]\n"
curl -s "http://localhost:5001/api/export-status?directory=../../Documents" -w "  [HTTP %{http_code}]\n"
```

All three must respond `400 Invalid directory` and leave `taxes.txt` intact. If
any returns `200` or deletes the file, the traversal guard (`safePhotoPath`) has
regressed. See `TestSafePhotoPath` in `backend-go/main_test.go` for the unit-level
version of this check.

Stop the dev server when done: `pkill -f 'camera-rip -dev'`.

---

## Step 8 — Verify live import progress in a browser (optional)

The API stream (Step 4b) proves progress events fire. To confirm the frontend
renders a moving progress bar, drive the **full** binary in a real browser.

First make the import large enough to observe. Save `$WORK/mkbig/main.go`:

```go
// Generates many ~200KB JPGs so an import takes long enough to watch the bar.
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
)

func main() {
	root := os.Args[1]
	perFolder, _ := strconv.Atoi(os.Args[2]) // e.g. 400 -> 800 total
	for _, dir := range []string{"100CANON", "101CANON"} {
		p := filepath.Join(root, "DCIM", dir)
		os.MkdirAll(p, 0755)
		for i := 1; i <= perFolder; i++ {
			img := image.NewRGBA(image.Rect(0, 0, 400, 300))
			r := rand.New(rand.NewSource(int64(i)))
			for y := 0; y < 300; y++ {
				for x := 0; x < 400; x++ {
					img.Set(x, y, color.RGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255})
				}
			}
			var buf bytes.Buffer
			jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92})
			os.WriteFile(filepath.Join(p, fmt.Sprintf("IMG_%04d.JPG", i)), buf.Bytes(), 0644)
		}
	}
	fmt.Println("wrote", perFolder*2, "jpgs")
}
```

```bash
rm -rf "$CARD"
cd "$WORK/mkbig" && go mod init mkbig 2>/dev/null; go run . "$CARD" 400   # 800 JPGs

rm -rf "$FAKE_HOME"/Pictures     # start the library empty
HOME="$FAKE_HOME" USER="$USER" "$WORK/camera-rip-full" &   # full binary serves the UI at :5001
sleep 1
```

Open `http://localhost:5001` in a browser and click **Import**. You should see the
"Importing..." button disable and a green progress bar with an `N / total files`
counter fill from 0 to 100%, then a success toast and the new directory selected
in the dropdown.

### Automating it with Playwright (headless)

If a Chromium build and `playwright-core` are available (this repo's cloud
environment ships Chromium under `/opt/pw-browsers`), you can script the check.
Save `$WORK/drive.js`:

```js
const { chromium } = require('playwright-core');
(async () => {
  const browser = await chromium.launch({
    // Point at your Chromium build:
    executablePath: process.env.CHROME || '/opt/pw-browsers/chromium-1194/chrome-linux/chrome',
    args: ['--no-sandbox'],
  });
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await page.goto('http://localhost:5001', { waitUntil: 'networkidle' });

  await page.locator('button.import-button').click();
  const frames = [];
  for (let i = 0; i < 80; i++) {
    const label = page.locator('.import-progress-label').first();
    if (await label.count() && await label.isVisible().catch(() => false)) {
      const text = (await label.textContent().catch(() => '')) || '';
      const fill = await page.locator('.import-progress-fill').first()
        .evaluate((el) => el.style.width).catch(() => '');
      frames.push(`${text.trim()} | fill=${fill}`);
    }
    if (!(await page.locator('button.import-button').isDisabled().catch(() => false)) && i > 2) break;
    await page.waitForTimeout(80);
  }
  const toast = page.locator('.Toastify__toast--success');
  await toast.waitFor({ state: 'visible', timeout: 8000 }).catch(() => {});
  console.log(JSON.stringify({
    progressFrames: frames.slice(0, 6),
    toast: (await toast.textContent().catch(() => '')) || '(none)',
    directory: await page.locator('select.directory-selector').inputValue().catch(() => '(none)'),
    consoleErrors: errors,
  }, null, 2));
  await browser.close();
})();
```

```bash
cd "$WORK" && npm init -y >/dev/null && PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm i playwright-core >/dev/null
node "$WORK/drive.js"
```

Expected: `progressFrames` showing increasing counts and `fill` widths (e.g.
`"432 / 800 files | fill=57%"`), a success toast, a selected `directory`, and an
empty `consoleErrors` array.

Stop the server: `pkill -f 'camera-rip-full'`.

---

## Step 9 — Automated checks (run these too)

The manual steps complement, but don't replace, the CI gates:

```bash
cd "$REPO/backend-go" && gofmt -s -l . && go vet ./... && go test ./...
cd "$REPO/frontend"   && CI=true npx react-scripts test --watchAll=false && CI=true npm run build
```

---

## Cleanup

```bash
pkill -f 'camera-rip' 2>/dev/null
rm -rf "$WORK" "$CARD"
```

---

## Notes & gotchas

- **Mount detection differs by OS.** Linux scans `/media/$USER/`; macOS scans
  `/Volumes/`. A folder only counts as a camera if it contains
  `DCIM/<3 digits><BRAND>` (e.g. `100CANON`). Brands live in the `supportedBrands`
  table in `main.go`.
- **Only one card is used at a time.** `findUSBMountPoint()` returns the *first*
  mount point that contains a camera DCIM folder (directory order from the OS,
  effectively alphabetical). If you leave a previous simulated card mounted
  alongside a new one, imports may silently read the wrong card. Remove old cards
  (`rm -rf /media/$USER/OLD_CARD`) before each test so only the intended one
  remains.
- **Date filtering uses filesystem mtime**, not EXIF capture time. When testing
  `since`/`until`, set file mtimes with `touch -d` on the card files rather than
  assuming the embedded date.
- **Synthetic CR3s are not real Canon files.** They validate the extract/export
  plumbing, but for preview-quality or CR3-parsing changes, test with a genuine
  CR3 from a Canon body.
- **The `-dev` binary does not serve the React app** (API only, port 5001). Use
  the full binary (with embedded frontend) for any browser-based check.
- **Always run against a throwaway `HOME`.** The server writes to
  `$HOME/Pictures/photos`; testing with your real `HOME` will litter your actual
  photo library.
```
