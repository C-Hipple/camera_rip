package main

// Focus detection: finds the photos with nothing in focus anywhere in the
// frame, so the missed shots from a wildlife shoot can be marked for deletion
// in one pass. Nothing here touches a file. The frontend marks whatever this
// reports, and deleting still goes through the usual review and confirmation.
//
// A photo is reduced to its luma at a fixed working size, so frames from any
// camera are measured on the same scale and most sensor noise is averaged
// away, then lightly smoothed and scored in overlapping tiles. In each tile
// the energy of the Laplacian is divided by the energy of the gradient. Both
// grow with the square of the contrast and with how much edge the tile holds,
// so the ratio cancels exposure and subject matter and is left measuring how
// wide the edges are: a crisp edge scores several times higher than the same
// edge defocused. A photo scores its best tile, which is what keeps a small
// sharp bird against a smooth, deliberately blurred background from being
// called out of focus.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/nfnt/resize"
)

const (
	// Long edge, in pixels, every photo is reduced to before it is measured.
	// A totally missed focus still smears an edge across several pixels at
	// this size, while the five- or six-fold reduction from a camera frame
	// averages most sensor noise away. A frame that is merely a touch soft
	// comes out sharp here, on purpose: the detector is after the shots nobody
	// would keep, not a pixel-peeping cull.
	focusWorkingSize = 1024

	// Side of the square tiles a frame is scored in, at the working size. They
	// step by half a tile, so a subject straddling a boundary still fills one.
	focusTileSize = 48

	// Mean squared gradient a tile needs before it is scored. Clear sky and
	// smooth bokeh have almost none, and their ratio is noise over noise,
	// which says nothing about focus.
	focusMinGradient = 4.0

	// Photos whose best tile scores below this are reported as blurry. It was
	// calibrated on real photographs defocused with a disc kernel at the
	// working size: in-focus frames, underexposed, noisy and small-subject ones
	// included, scored 0.87 and up and a pixel of defocus about 0.7 and up,
	// while three pixels (a 35-pixel blur on a 24 MP frame) mostly scored under
	// 0.5. It errs towards keeping a doubtful photo.
	blurThreshold = 0.6

	// Shortest long edge a photo can have and still be judged. The preview
	// embedded in some raw files is a 160x120 thumbnail, far too small to say
	// anything about focus, so those are reported unreadable rather than
	// guessed at.
	minFocusEdge = 320

	// Upper bound on photos decoded at once. A 24 MP frame takes about 36 MB
	// decoded, so this caps the memory the check can hold.
	maxFocusWorkers = 8
)

// decodePhoto reads the photo at path, going through the embedded JPEG
// preview for a raw file.
func decodePhoto(path string) (image.Image, error) {
	if isRawFile(path) {
		jpegData, err := extractEmbeddedJPEG(path)
		if err != nil {
			return nil, fmt.Errorf("extracting embedded JPEG from %s: %w", filepath.Base(path), err)
		}
		img, err := jpeg.Decode(bytes.NewReader(jpegData))
		if err != nil {
			return nil, fmt.Errorf("decoding embedded JPEG from %s: %w", filepath.Base(path), err)
		}
		return img, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	return img, err
}

// lumaImage returns img's brightness as a greyscale image. A JPEG's Y plane
// already is exactly that, so it is wrapped rather than copied.
func lumaImage(img image.Image) *image.Gray {
	switch src := img.(type) {
	case *image.Gray:
		return src
	case *image.YCbCr:
		return &image.Gray{Pix: src.Y, Stride: src.YStride, Rect: src.Rect}
	}
	b := img.Bounds()
	gray := image.NewGray(b)
	draw.Draw(gray, b, img, b.Min, draw.Src)
	return gray
}

// focusWorkingImage is img's luma with its long edge reduced to
// focusWorkingSize. A smaller photo is measured as it is. Lanczos keeps edges
// as crisp as an ideal reduction would; a softer filter reads every photo a
// little blurrier than the calibration behind blurThreshold assumed.
func focusWorkingImage(img image.Image) *image.Gray {
	gray := lumaImage(img)
	w, h := gray.Bounds().Dx(), gray.Bounds().Dy()
	if w <= focusWorkingSize && h <= focusWorkingSize {
		return gray
	}
	var rw, rh uint
	if w >= h {
		rw = focusWorkingSize
	} else {
		rh = focusWorkingSize
	}
	return lumaImage(resize.Resize(rw, rh, gray, resize.Lanczos3))
}

// focusScore rates how sharp the sharpest part of a photo is, as described at
// the top of this file. A frame with no tile detailed enough to judge scores
// zero: there is nothing in it in focus.
func focusScore(img image.Image) float64 {
	g := focusWorkingImage(img)
	b := g.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 3 || h < 3 {
		return 0
	}

	// A 1-2-1 binomial pass in each direction knocks the remaining pixel noise
	// down before differencing, which would otherwise amplify it. Edges clamp.
	at := func(x, y int) float64 {
		x = min(max(x, 0), w-1)
		y = min(max(y, 0), h-1)
		return float64(g.Pix[y*g.Stride+x])
	}
	smooth := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := 0.0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					s += float64((2-absInt(dx))*(2-absInt(dy))) * at(x+dx, y+dy)
				}
			}
			smooth[y*w+x] = s / 16
		}
	}

	// Accumulate the squared Laplacian and squared gradient into half-tile
	// cells over the interior, so each tile is just its 2x2 block of cells.
	cell := focusTileSize / 2
	iw, ih := w-2, h-2
	cols, rows := (iw+cell-1)/cell, (ih+cell-1)/cell
	lap := make([]float64, cols*rows)
	grad := make([]float64, cols*rows)
	count := make([]float64, cols*rows)
	for y := 1; y < h-1; y++ {
		row := ((y - 1) / cell) * cols
		for x := 1; x < w-1; x++ {
			c := smooth[y*w+x]
			n, s := smooth[(y-1)*w+x], smooth[(y+1)*w+x]
			wst, e := smooth[y*w+x-1], smooth[y*w+x+1]
			l := 4*c - n - s - wst - e
			gx, gy := (e-wst)/2, (s-n)/2
			i := row + (x-1)/cell
			lap[i] += l * l
			grad[i] += gx*gx + gy*gy
			count[i]++
		}
	}

	// A frame only one cell across (or down) gets one-cell-wide tiles.
	best := 0.0
	for r := 0; r < max(rows-1, 1); r++ {
		for c := 0; c < max(cols-1, 1); c++ {
			var tl, tg, tn float64
			for rr := r; rr <= min(r+1, rows-1); rr++ {
				for cc := c; cc <= min(c+1, cols-1); cc++ {
					i := rr*cols + cc
					tl, tg, tn = tl+lap[i], tg+grad[i], tn+count[i]
				}
			}
			if tn == 0 || tg/tn < focusMinGradient {
				continue
			}
			best = math.Max(best, tl/tg)
		}
	}
	return best
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// photoFocusScore decodes and scores one photo on disk.
func photoFocusScore(path string) (float64, error) {
	img, err := decodePhoto(path)
	if err != nil {
		return 0, err
	}
	if b := img.Bounds(); max(b.Dx(), b.Dy()) < minFocusEdge {
		return 0, fmt.Errorf("%dx%d is too small to judge focus", b.Dx(), b.Dy())
	}
	return focusScore(img), nil
}

type focusResult struct {
	name  string
	score float64
	err   error
}

// detectBlurHandler scores the focus of the listed photos in a session and
// reports the ones with nothing sharp in them. It never changes a file: the
// frontend marks what comes back for deletion and the user reviews it there.
//
// Like an import, the response streams NDJSON so a big shoot shows progress:
// {"type":"start","total":n}, then {"type":"progress","checked":k,"total":n},
// then {"type":"done","checked":n,"blurry":[...],"failed":[...],
// "scores":{...},"threshold":t}. A photo that cannot be read is listed under
// failed, never under blurry. Closing the request stops the work.
func detectBlurHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var data struct {
		Directory string   `json:"directory"`
		Files     []string `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !validDirName(data.Directory) {
		http.Error(w, "Invalid directory", http.StatusBadRequest)
		return
	}
	targetDir, err := safePhotoPath(data.Directory)
	if err != nil {
		http.Error(w, "Invalid directory", http.StatusBadRequest)
		return
	}
	if info, err := os.Stat(targetDir); err != nil || !info.IsDir() {
		http.Error(w, "Directory not found", http.StatusNotFound)
		return
	}

	files := make([]string, 0, len(data.Files))
	seen := make(map[string]bool, len(data.Files))
	for _, name := range data.Files {
		if !validPhotoName(name) {
			http.Error(w, "Invalid photo name", http.StatusBadRequest)
			return
		}
		if !seen[name] {
			seen[name] = true
			files = append(files, name)
		}
	}
	if len(files) == 0 {
		http.Error(w, "No photos to check", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	emit := func(event map[string]interface{}) {
		if err := enc.Encode(event); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	total := len(files)
	emit(map[string]interface{}{"type": "start", "total": total})

	ctx := r.Context()
	jobs := make(chan string)
	results := make(chan focusResult)
	go func() {
		defer close(jobs)
		for _, name := range files {
			select {
			case jobs <- name:
			case <-ctx.Done():
				return
			}
		}
	}()
	var wg sync.WaitGroup
	for i := 0; i < min(runtime.NumCPU(), maxFocusWorkers, total); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range jobs {
				score, err := photoFocusScore(filepath.Join(targetDir, name))
				results <- focusResult{name: name, score: score, err: err}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	// Throttle progress events to at most ~100 over the whole check.
	step := max(total/100, 1)
	checked := 0
	blurry := []string{}
	failed := []string{}
	scores := make(map[string]float64, total)
	for res := range results {
		checked++
		if res.err != nil {
			log.Printf("Focus check could not read %s: %v", res.name, res.err)
			failed = append(failed, res.name)
		} else {
			scores[res.name] = math.Round(res.score*1000) / 1000
			if res.score < blurThreshold {
				blurry = append(blurry, res.name)
			}
		}
		if checked == total || checked%step == 0 {
			emit(map[string]interface{}{"type": "progress", "checked": checked, "total": total})
		}
	}
	if ctx.Err() != nil {
		log.Printf("Focus check of %s cancelled after %d of %d photos", data.Directory, checked, total)
		return
	}

	sort.Strings(blurry)
	sort.Strings(failed)
	log.Printf("Focus check of %s: %d of %d photos look blurry (%d unreadable)", data.Directory, len(blurry), total, len(failed))
	emit(map[string]interface{}{
		"type":      "done",
		"checked":   checked,
		"blurry":    blurry,
		"failed":    failed,
		"scores":    scores,
		"threshold": blurThreshold,
	})
}
