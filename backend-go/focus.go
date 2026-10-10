package main

// Focus detection: finds the photos with nothing in focus anywhere in the
// frame, so the missed shots from a wildlife shoot can be marked for deletion
// in one pass. Nothing here touches a file. The frontend marks whatever this
// reports, and deleting still goes through the usual review and confirmation.
//
// A photo is judged by how wide its edges are. Defocus spreads every edge
// over a distance that grows with the miss, and an edge's width, its contrast
// divided by its steepest slope, measures that spread in pixels whatever the
// edge belongs to and however bright it is. Only strong edges are measured:
// the outline of a bird against the sky, or of a twig against the leaves
// behind it. Faint ones are where sensor noise and the camera's own noise
// reduction pass for detail, and at high ISO that mottling is crisp enough to
// make a missed frame look sharp. The frame is scored in overlapping tiles,
// each by the width of its narrowest edges, and a photo scores its best tile.
// That is what keeps a small sharp bird against a smooth, deliberately blurred
// background from being called out of focus: the bird's tile is sharp, and
// nothing else has to be.
//
// Everything is measured on the photo's own pixels. Shrinking a frame first
// shrinks a missed shot's blur towards the width of an in-focus edge, leaving
// too little between them to judge by; cropping keeps the scale, so an edited
// photo is judged the same as its original.

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
)

const (
	// Contrast, in grey levels, an edge needs before its width is measured:
	// about a quarter of the tonal range. The outline of a bird or a branch
	// clears it easily; noise, and the blotches noise reduction leaves behind
	// at high ISO, stay well under it.
	focusEdgeContrast = 70

	// Brightness range, in grey levels, that the most contrasty patch of a
	// clear, well-exposed frame spans at least. Haze and underexposure flatten
	// every edge in a frame alike, so a frame whose strongest patch falls
	// short of this has focusEdgeContrast scaled down in proportion, though
	// never below half. A squirrel in morning fog is then judged by its own
	// dulled edges instead of written off for having none strong enough.
	focusFullContrast = 160

	// Widest edge, in pixels, that counts towards a tile's score. Anything
	// wider is blurry whatever else the tile holds, and a photo with no tile
	// of narrower edges scores this: there is nothing in it in focus.
	focusMaxEdgeWidth = 9.0

	// A line thinner than the blur, a twig or a stalk of grass, loses contrast
	// as it spreads, so it reads narrower than an edge defocused as much. A
	// line's width is scaled up by this to match.
	focusLineFactor = 1.5

	// Side, in pixels, of the square tiles a frame is scored in. They step by
	// half a tile, so a subject straddling a boundary still fills one.
	focusTileSize = 256

	// Strong edges a tile needs before it is judged, and the rank of the one
	// whose width is its score: the 150th narrowest. A rank this deep keeps a
	// few stray specks from passing a missed frame off as sharp, and a tile
	// with fewer strong edges says too little to go on.
	focusTileEdges = 150

	// Photos whose best tile's edges are at least this wide, in pixels, are
	// reported blurry. It was calibrated on 24 MP shoots from a Canon R10 with
	// an RF 200-800mm, ISO 160 to 20000: in-focus frames scored up to 3.9, soft
	// but keepable ones (heat shimmer over a marsh, a heron in deep shade) up
	// to 5.2, and the missed ones 5.7 and up, most of them over 6. It errs
	// towards keeping a doubtful photo. A camera with finer pixels spreads the
	// same blur over more of them, and would want it raised.
	blurThreshold = 5.5

	// Shortest long edge a photo can have and still be judged. The preview
	// embedded in some raw files is a 160x120 thumbnail, far too small to say
	// anything about focus, so those are reported unreadable rather than
	// guessed at.
	minFocusEdge = 320

	// Upper bound on photos measured at once. A 24 MP frame takes about 36 MB
	// decoded and 48 MB more while it is measured, so this caps the memory the
	// check can hold.
	maxFocusWorkers = 8
)

// Distances, in pixels, either side of an edge at which the profile across it
// is sampled. The contrast is the largest difference between a pair, which
// reaches the full step for any edge narrow enough to count.
var focusProfileSteps = [...]float64{1, 1.5, 2, 3, 4, 6, 8, 12}

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

// focusReading is a photo's focus score and where it came from: the centre of
// its sharpest tile, as fractions of the frame's width and height.
type focusReading struct {
	score float64
	x, y  float64
}

// focusScore is the width, in pixels, of the edges in the sharpest part of a
// photo, as described at the top of this file. The higher it is, the blurrier
// the photo. A frame with no tile of strong, narrow edges scores
// focusMaxEdgeWidth: there is nothing in it in focus.
func focusScore(img image.Image) float64 {
	return focusMeasure(img).score
}

// focusField is a photo's luma after a 1-2-1 binomial pass in each direction,
// which keeps pixel noise from dominating the slopes. It is stored at 16 times
// scale, which holds the pass exactly.
type focusField struct {
	w, h int
	pix  []uint16
}

func newFocusField(g *image.Gray) *focusField {
	b := g.Bounds()
	w, h := b.Dx(), b.Dy()
	f := &focusField{w: w, h: h, pix: make([]uint16, w*h)}
	// Across each row first, edges clamped.
	for y := 0; y < h; y++ {
		src := g.Pix[y*g.Stride : y*g.Stride+w]
		dst := f.pix[y*w : (y+1)*w]
		for x := range dst {
			dst[x] = uint16(src[max(x-1, 0)]) + 2*uint16(src[x]) + uint16(src[min(x+1, w-1)])
		}
	}
	// Then down each column, in place, keeping the row above as it was.
	above, row := make([]uint16, w), make([]uint16, w)
	copy(above, f.pix[:w])
	for y := 0; y < h; y++ {
		copy(row, f.pix[y*w:(y+1)*w])
		below := row
		if y+1 < h {
			below = f.pix[(y+1)*w : (y+2)*w]
		}
		dst := f.pix[y*w : (y+1)*w]
		for x := range dst {
			dst[x] = above[x] + 2*row[x] + below[x]
		}
		above, row = row, above
	}
	return f
}

// at is the smoothed luma at a fractional position, in grey levels,
// interpolated between the four pixels around it and clamped to the frame.
func (f *focusField) at(x, y float64) float64 {
	x = math.Max(0, math.Min(float64(f.w-1), x))
	y = math.Max(0, math.Min(float64(f.h-1), y))
	x0, y0 := min(int(x), f.w-2), min(int(y), f.h-2)
	fx, fy := x-float64(x0), y-float64(y0)
	i := y0*f.w + x0
	top := float64(f.pix[i])*(1-fx) + float64(f.pix[i+1])*fx
	bottom := float64(f.pix[i+f.w])*(1-fx) + float64(f.pix[i+f.w+1])*fx
	return (top*(1-fy) + bottom*fy) / 16
}

// strongestContrast is the largest range of brightness, in grey levels,
// inside any one cell of the field. A cell is far wider than any blur this
// looks for, so defocus moves an edge's brightness around inside it without
// shrinking the range.
func (f *focusField) strongestContrast(cell int) float64 {
	cols := (f.w + cell - 1) / cell
	lo, hi := make([]uint16, cols), make([]uint16, cols)
	strongest := 0
	for y := 0; y < f.h; y++ {
		if y%cell == 0 {
			for c := range lo {
				lo[c], hi[c] = math.MaxUint16, 0
			}
		}
		row := f.pix[y*f.w : (y+1)*f.w]
		for x, v := range row {
			c := x / cell
			lo[c], hi[c] = min(lo[c], v), max(hi[c], v)
		}
		if y%cell == cell-1 || y == f.h-1 {
			for c := range lo {
				strongest = max(strongest, int(hi[c])-int(lo[c]))
			}
		}
	}
	return float64(strongest) / 16
}

// gradient is the central difference at pixel i, at 32 times the slope in grey
// levels per pixel: 16 from the field's scale, 2 from spanning two pixels.
func (f *focusField) gradient(i int) (gx, gy int32) {
	return int32(f.pix[i+1]) - int32(f.pix[i-1]), int32(f.pix[i+f.w]) - int32(f.pix[i-f.w])
}

// edgeWidth measures the edge through (x, y), whose gradient is (gx, gy) with
// magnitude m (both at the gradient's scale), returning its width in pixels
// and its contrast in grey levels. The width is the contrast over the slope,
// with a line's scaled by focusLineFactor.
func (f *focusField) edgeWidth(x, y int, gx, gy int32, m float64) (width, contrast float64) {
	nx, ny := float64(gx)/m, float64(gy)/m
	var up, down [len(focusProfileSteps)]float64
	peak := 0
	for k, d := range focusProfileSteps {
		up[k] = f.at(float64(x)+d*nx, float64(y)+d*ny)
		down[k] = f.at(float64(x)-d*nx, float64(y)-d*ny)
		if c := up[k] - down[k]; c > contrast {
			contrast, peak = c, k
		}
	}
	if contrast <= 0 {
		return math.Inf(1), 0
	}
	width = contrast / (m / 32)

	// A step stays at its new level beyond the edge; a line comes back. Look
	// for the profile falling back by half the contrast on either side,
	// within a few widths of the edge.
	reach := 2.5 * width
	top, bottom := up[peak], down[peak]
	for k, d := range focusProfileSteps {
		if d > reach {
			break
		}
		top, bottom = math.Max(top, up[k]), math.Min(bottom, down[k])
	}
	for k, d := range focusProfileSteps {
		if d > reach {
			break
		}
		if k > peak && (top-up[k] > contrast/2 || down[k]-bottom > contrast/2) {
			return width * focusLineFactor, contrast
		}
	}
	return width, contrast
}

// focusMeasure is focusScore, along with where in the frame the score came
// from, which is what tells a sharp bird from a sharp twig when tuning.
func focusMeasure(img image.Image) focusReading {
	none := focusReading{score: focusMaxEdgeWidth}
	g := lumaImage(img)
	b := g.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 5 || h < 5 {
		return none
	}
	f := newFocusField(g)
	cell := focusTileSize / 2
	minContrast := focusEdgeContrast * math.Max(0.5, math.Min(1, f.strongestContrast(cell)/focusFullContrast))

	// Each half-tile cell keeps a histogram of the widths of the strong edges
	// in it, so a tile can rank its edges from its 2x2 block of cells without
	// sorting.
	const binsPerPixel = 50
	bins := int(focusMaxEdgeWidth * binsPerPixel)
	cols, rows := (w+cell-1)/cell, (h+cell-1)/cell
	hist := make([]uint32, cols*rows*bins)

	// An edge narrower than focusMaxEdgeWidth with minContrast across it is
	// at least this steep, so most of the frame is ruled out by its slope
	// alone.
	minGradient := 32 * minContrast / focusMaxEdgeWidth
	minSquared := int32(minGradient * minGradient)
	for y := 2; y < h-2; y++ {
		for x := 2; x < w-2; x++ {
			i := y*w + x
			gx, gy := f.gradient(i)
			m2 := gx*gx + gy*gy
			if m2 < minSquared {
				continue
			}
			// Only the steepest point across an edge measures it, so the
			// pixel must be at least as steep as its neighbours on either
			// side, along the gradient to the nearest 45 degrees.
			ax, ay := math.Abs(float64(gx)), math.Abs(float64(gy))
			var o int
			switch {
			case ax > 2.414*ay:
				o = 1
			case ay > 2.414*ax:
				o = w
			case (gx > 0) == (gy > 0):
				o = w + 1
			default:
				o = w - 1
			}
			ax2, ay2 := f.gradient(i - o)
			bx2, by2 := f.gradient(i + o)
			if m2 < ax2*ax2+ay2*ay2 || m2 < bx2*bx2+by2*by2 {
				continue
			}
			width, contrast := f.edgeWidth(x, y, gx, gy, math.Sqrt(float64(m2)))
			if contrast < minContrast || width >= focusMaxEdgeWidth {
				continue
			}
			c := (y/cell)*cols + x/cell
			hist[c*bins+int(width*binsPerPixel)]++
		}
	}

	// A frame only one cell across (or down) gets one-cell-wide tiles.
	best := none
	for r := 0; r < max(rows-1, 1); r++ {
		for c := 0; c < max(cols-1, 1); c++ {
			count := 0
			for bin := 0; bin < bins; bin++ {
				for rr := r; rr <= min(r+1, rows-1); rr++ {
					for cc := c; cc <= min(c+1, cols-1); cc++ {
						count += int(hist[(rr*cols+cc)*bins+bin])
					}
				}
				if count < focusTileEdges {
					continue
				}
				if width := (float64(bin) + 0.5) / binsPerPixel; width < best.score {
					// The tile spans two cells, or what is left of the frame.
					cx := c*cell + min(2*cell, w-c*cell)/2
					cy := r*cell + min(2*cell, h-r*cell)/2
					best = focusReading{score: width, x: float64(cx) / float64(w), y: float64(cy) / float64(h)}
				}
				break
			}
		}
	}
	return best
}

// photoFocus decodes and measures one photo on disk.
func photoFocus(path string) (focusReading, error) {
	img, err := decodePhoto(path)
	if err != nil {
		return focusReading{}, err
	}
	if b := img.Bounds(); max(b.Dx(), b.Dy()) < minFocusEdge {
		return focusReading{}, fmt.Errorf("%dx%d is too small to judge focus", b.Dx(), b.Dy())
	}
	return focusMeasure(img), nil
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
// "scores":{...},"threshold":t}, where each score is a photo's edge width in
// pixels and the blurry ones are those at or above the threshold. A photo
// that cannot be read is listed under failed, never under blurry. Closing the
// request stops the work.
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
				reading, err := photoFocus(filepath.Join(targetDir, name))
				results <- focusResult{name: name, score: reading.score, err: err}
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
			if res.score >= blurThreshold {
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
