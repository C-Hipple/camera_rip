---
name: verify
description: Build, launch, and drive the Camera Rip app headlessly to verify a change end-to-end.
---

# Verifying Camera Rip changes

## Build & launch

```bash
make build                      # frontend build → embed → backend-go/camera-rip
./backend-go/camera-rip &       # serves app + API on :5001
```

No SD card is needed to exercise the review UI. The server lists sessions from
`~/Pictures/photos/{session}/`, so stage fake data:

```bash
mkdir -p ~/Pictures/photos/2026-01-01-1200
# any JPEGs work; generate with Go stdlib (image/jpeg) if none available —
# a gradient + grid pattern makes zoom/pan position visible in screenshots
```

`GET /api/directories` and `GET /api/photos?directory=...` confirm the staging
worked before driving the UI.

## Drive

Playwright (`playwright-core` + `executablePath: '/opt/pw-browsers/chromium'`
in remote sessions) against `http://localhost:5001`:

- The first directory auto-loads; wait for `.photo-display`.
- Keyboard: `s` select, `x` unselect, `d` delete-mark, `k`/`j` next/prev,
  `f` fullscreen, `h` pin.
- Real wheel/pinch input: CDP `Input.dispatchMouseEvent` type `mouseWheel`
  (`modifiers: 2` = Ctrl ≙ macOS trackpad pinch). React synthetic events don't
  exercise listener passivity — use CDP for anything preventDefault-related.
- Read zoom/pan state from `getComputedStyle(img).transform` (matrix scale/tx/ty).

## Gotchas

- Headless Chromium does not implement ctrl+wheel browser page zoom, so
  "page didn't zoom" needs the defaultPrevented check on a cancelable
  WheelEvent as corroborating evidence.
- On first load the film strip requests `/thumbnail/{dir}/undefined` (500 in
  server log + broken image) — pre-existing, clears after navigating.
