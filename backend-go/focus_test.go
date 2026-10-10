package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// sceneSpec places the parts of a test photo in fractions of the frame, so the
// same scene can be drawn at any resolution.
type sceneSpec struct {
	birdRadius float64 // of the frame width; 0 leaves the bird out
	branch     bool    // a dark diagonal branch across the lower frame
}

// cellHash is a repeatable pseudo-random value in [0, 1) for a texture cell.
func cellHash(x, y int) float64 {
	h := uint32(x)*374761393 + uint32(y)*668265263
	h = (h ^ (h >> 13)) * 1274126177
	return float64(h^(h>>16)) / float64(1<<32)
}

// sceneValue is the scene's brightness at (u, v), both in [0, 1).
func sceneValue(spec sceneSpec, u, v, aspect float64) float64 {
	// A clear sky, brighter towards the horizon.
	val := 140 + 70*v
	if spec.branch && math.Abs(v-0.8+0.3*u) < 0.02 {
		val = 35
	}
	if spec.birdRadius > 0 {
		// Distances in units of the frame width, so the bird stays round.
		dx, dy := u-0.5, (v-0.45)/aspect
		if dx*dx+dy*dy*1.8 < spec.birdRadius*spec.birdRadius {
			// Feathers: cells about three pixels across at the working size.
			val = 45 + 130*cellHash(int(u*340), int(v*340/aspect))
			ex, ey := dx+0.4*spec.birdRadius, dy+0.15*spec.birdRadius
			if ex*ex+ey*ey < 0.0144*spec.birdRadius*spec.birdRadius*4 {
				val = 15 // the eye
				if ex*ex+ey*ey < 0.0016*spec.birdRadius*spec.birdRadius*4 {
					val = 245 // its catchlight
				}
			}
		}
	}
	return val
}

// renderScene draws spec at w x h, averaging ss x ss samples per pixel so the
// edges are anti-aliased the way a camera's are.
func renderScene(spec sceneSpec, w, h, ss int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	aspect := float64(h) / float64(w)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sum := 0.0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u := (float64(x) + (float64(sx)+0.5)/float64(ss)) / float64(w)
					v := (float64(y) + (float64(sy)+0.5)/float64(ss)) / float64(h)
					sum += sceneValue(spec, u, v, aspect)
				}
			}
			img.Pix[y*img.Stride+x] = uint8(math.Round(sum / float64(ss*ss)))
		}
	}
	return img
}

// defocus blurs img with a disc of the given radius, the shape a lens throws
// an out-of-focus point into.
func defocus(img *image.Gray, radius float64) *image.Gray {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewGray(b)
	r := int(math.Ceil(radius))
	type tap struct {
		dx, dy int
		w      float64
	}
	var taps []tap
	total := 0.0
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			// Weight the rim by how much of the pixel the disc covers.
			wt := math.Max(0, math.Min(1, radius+0.5-math.Hypot(float64(dx), float64(dy))))
			if wt > 0 {
				taps = append(taps, tap{dx, dy, wt})
				total += wt
			}
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := 0.0
			for _, t := range taps {
				xx, yy := min(max(x+t.dx, 0), w-1), min(max(y+t.dy, 0), h-1)
				s += t.w * float64(img.Pix[yy*img.Stride+xx])
			}
			out.Pix[y*out.Stride+x] = uint8(math.Round(s / total))
		}
	}
	return out
}

// darken scales every value, as an underexposed frame would.
func darken(img *image.Gray, k float64) *image.Gray {
	out := image.NewGray(img.Bounds())
	for i, v := range img.Pix {
		out.Pix[i] = uint8(math.Round(float64(v) * k))
	}
	return out
}

// addNoise adds repeatable Gaussian noise of the given standard deviation.
func addNoise(img *image.Gray, sigma float64) *image.Gray {
	rng := rand.New(rand.NewSource(1))
	out := image.NewGray(img.Bounds())
	for i, v := range img.Pix {
		out.Pix[i] = uint8(math.Max(0, math.Min(255, math.Round(float64(v)+rng.NormFloat64()*sigma))))
	}
	return out
}

var (
	wholeScene = sceneSpec{birdRadius: 0.12, branch: true}
	smallBird  = sceneSpec{birdRadius: 0.04}
)

// A frame that is in focus scores above the threshold, one that is a touch
// soft does too, and one whose focus was missed outright scores below it,
// with the score falling steadily as the defocus grows.
func TestFocusScoreTellsSharpFromDefocused(t *testing.T) {
	sharp := renderScene(wholeScene, 1024, 683, 2)
	scores := []float64{focusScore(sharp)}
	for _, radius := range []float64{1, 3, 6} {
		scores = append(scores, focusScore(defocus(sharp, radius)))
	}
	if scores[0] < blurThreshold {
		t.Errorf("in-focus frame scored %.3f, below the %.2f threshold", scores[0], blurThreshold)
	}
	// One pixel of defocus at the working size is about six on a 24 MP
	// frame: soft at 100%, but nobody's idea of a missed shot.
	if scores[1] < blurThreshold {
		t.Errorf("slightly soft frame scored %.3f, below the %.2f threshold", scores[1], blurThreshold)
	}
	if scores[3] >= blurThreshold {
		t.Errorf("badly defocused frame scored %.3f, not below the %.2f threshold", scores[3], blurThreshold)
	}
	for i := 1; i < len(scores); i++ {
		if scores[i] >= scores[i-1] {
			t.Errorf("scores %v do not fall as the defocus grows", scores)
		}
	}
}

// The wildlife case: a small bird sharp against a plain sky is in focus, even
// though nearly the whole frame has no detail at all.
func TestFocusScoreFindsASmallSharpSubject(t *testing.T) {
	sharp := renderScene(smallBird, 1024, 683, 2)
	if got := focusScore(sharp); got < blurThreshold {
		t.Errorf("small sharp subject scored %.3f, below the %.2f threshold", got, blurThreshold)
	}
	if got := focusScore(defocus(sharp, 4)); got >= blurThreshold {
		t.Errorf("small defocused subject scored %.3f, not below the %.2f threshold", got, blurThreshold)
	}
	// A frame of nothing but out-of-focus background has nothing sharp in it.
	bokeh := defocus(renderScene(sceneSpec{branch: true}, 1024, 683, 2), 6)
	if got := focusScore(bokeh); got >= blurThreshold {
		t.Errorf("background-only frame scored %.3f, not below the %.2f threshold", got, blurThreshold)
	}
}

// A reading says where its score came from: on the small bird, not the sky.
func TestFocusMeasureLocatesTheSharpestTile(t *testing.T) {
	r := focusMeasure(renderScene(smallBird, 1024, 683, 2))
	if r.score != focusScore(renderScene(smallBird, 1024, 683, 2)) {
		t.Errorf("focusMeasure scored %.3f, focusScore disagrees", r.score)
	}
	// The bird sits at 50% across and 45% down; a tile is 48 of 1024 pixels.
	if math.Abs(r.x-0.5) > 0.06 || math.Abs(r.y-0.45) > 0.08 {
		t.Errorf("sharpest tile at %.0f%%,%.0f%%, want on the bird at 50%%,45%%", 100*r.x, 100*r.y)
	}
}

// Exposure scales the Laplacian and the gradient alike, so an underexposed
// frame scores as its correctly exposed twin does; and the noise of a
// high-ISO frame neither hides a sharp subject nor sharpens a missed one.
func TestFocusScoreIgnoresExposureAndNoise(t *testing.T) {
	sharp := renderScene(wholeScene, 1024, 683, 2)
	base := focusScore(sharp)
	if got := focusScore(darken(sharp, 0.3)); math.Abs(got-base) > 0.05*base {
		t.Errorf("underexposed frame scored %.3f, want within 5%% of %.3f", got, base)
	}
	if got := focusScore(addNoise(sharp, 2)); got < blurThreshold {
		t.Errorf("noisy in-focus frame scored %.3f, below the %.2f threshold", got, blurThreshold)
	}
	if got := focusScore(addNoise(defocus(sharp, 6), 1)); got >= blurThreshold {
		t.Errorf("noisy defocused frame scored %.3f, not below the %.2f threshold", got, blurThreshold)
	}
}

// Every photo is measured at the working size, so the same scene scores the
// same whether the camera recorded it at 2048 or 4096 pixels across.
func TestFocusScoreIsResolutionIndependent(t *testing.T) {
	for _, spec := range []sceneSpec{wholeScene, smallBird} {
		ref := focusScore(renderScene(spec, 1024, 683, 2))
		for _, w := range []int{2048, 4096} {
			got := focusScore(renderScene(spec, w, w*2/3, 1))
			if math.Abs(got-ref) > 0.1*ref {
				t.Errorf("%+v at %dpx scored %.3f, want within 10%% of %.3f at the working size", spec, w, got, ref)
			}
		}
	}
}

func TestFocusScoreOfAFeaturelessFrame(t *testing.T) {
	flat := image.NewGray(image.Rect(0, 0, 640, 480))
	for i := range flat.Pix {
		flat.Pix[i] = 128
	}
	if got := focusScore(flat); got != 0 {
		t.Errorf("flat frame scored %.3f, want 0", got)
	}
	if got := focusScore(renderScene(sceneSpec{}, 640, 480, 1)); got != 0 {
		t.Errorf("empty sky scored %.3f, want 0", got)
	}
}

func TestLumaImage(t *testing.T) {
	// A JPEG's Y plane is its luma already: it is shared, not copied.
	ycc := image.NewYCbCr(image.Rect(0, 0, 4, 3), image.YCbCrSubsampleRatio420)
	ycc.Y[ycc.YOffset(2, 1)] = 200
	gray := lumaImage(ycc)
	if got := gray.GrayAt(2, 1).Y; got != 200 {
		t.Fatalf("luma at (2,1) = %d, want 200", got)
	}
	ycc.Y[ycc.YOffset(3, 2)] = 77
	if got := gray.GrayAt(3, 2).Y; got != 77 {
		t.Errorf("luma does not share the Y plane: got %d after writing 77", got)
	}

	// Anything else is converted.
	rgba := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	rgba.Set(1, 1, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	if got := lumaImage(rgba).GrayAt(1, 1).Y; got != 255 {
		t.Errorf("white converted to luma %d, want 255", got)
	}
}

func encodeTestJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A raw file's embedded thumbnail can be 160x120, too small to judge; the
// photo is reported unreadable rather than guessed at.
func TestPhotoFocusRefusesATinyPreview(t *testing.T) {
	path := writeTemp(t, encodeTestJPEG(t, renderScene(wholeScene, 160, 120, 1)))
	if _, err := photoFocus(path); err == nil {
		t.Error("a 160x120 photo was scored, want an error")
	}
}

// readNDJSON decodes every event in a streamed response.
func readNDJSON(t *testing.T, body []byte) []map[string]interface{} {
	t.Helper()
	var events []map[string]interface{}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		var evt map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("bad NDJSON line %q: %v", scanner.Text(), err)
		}
		events = append(events, evt)
	}
	return events
}

func TestDetectBlurHandler(t *testing.T) {
	sharp := renderScene(wholeScene, 1024, 683, 2)
	directory := withTestSession(t, "100_IMG_0001.JPG", encodeTestJPEG(t, sharp))
	dir := filepath.Join(photoBaseDir, directory)
	writes := map[string][]byte{
		"100_IMG_0002.JPG": encodeTestJPEG(t, defocus(sharp, 6)),
		"100_IMG_0003.JPG": []byte("not a photo"),
		"100_IMG_0004.JPG": encodeTestJPEG(t, renderScene(wholeScene, 160, 120, 1)),
	}
	for name, data := range writes {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}

	w := postJSON(t, detectBlurHandler, "/api/detect-blur",
		`{"directory":"`+directory+`","files":["100_IMG_0001.JPG","100_IMG_0002.JPG","100_IMG_0003.JPG","100_IMG_0004.JPG"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("detect-blur returned %d, want 200: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}

	events := readNDJSON(t, w.Body.Bytes())
	if len(events) < 3 {
		t.Fatalf("got %d events, want start, progress and done: %v", len(events), events)
	}
	if events[0]["type"] != "start" || events[0]["total"] != 4.0 {
		t.Errorf("first event = %v, want start of 4", events[0])
	}
	if p := events[len(events)-2]; p["type"] != "progress" || p["checked"] != 4.0 || p["total"] != 4.0 {
		t.Errorf("last progress event = %v, want 4 of 4 checked", p)
	}

	done := events[len(events)-1]
	if done["type"] != "done" || done["checked"] != 4.0 {
		t.Fatalf("final event = %v, want done with 4 checked", done)
	}
	if got, want := done["blurry"], []interface{}{"100_IMG_0002.JPG"}; !reflect.DeepEqual(got, want) {
		t.Errorf("blurry = %v, want %v", got, want)
	}
	// Unreadable photos are never reported blurry: they would be deleted
	// sight unseen if the user trusted the list.
	if got, want := done["failed"], []interface{}{"100_IMG_0003.JPG", "100_IMG_0004.JPG"}; !reflect.DeepEqual(got, want) {
		t.Errorf("failed = %v, want %v", got, want)
	}
	scores, _ := done["scores"].(map[string]interface{})
	if len(scores) != 2 || scores["100_IMG_0001.JPG"] == nil || scores["100_IMG_0002.JPG"] == nil {
		t.Errorf("scores = %v, want one for each readable photo", scores)
	}
	if done["threshold"] != blurThreshold {
		t.Errorf("threshold = %v, want %v", done["threshold"], blurThreshold)
	}

	// Checking focus only reports: every photo is still there afterwards.
	for _, name := range []string{"100_IMG_0001.JPG", "100_IMG_0002.JPG", "100_IMG_0003.JPG", "100_IMG_0004.JPG"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s is gone after the focus check: %v", name, err)
		}
	}
}

func TestDetectBlurHandlerRejectsBadRequests(t *testing.T) {
	directory := withTestSession(t, "100_IMG_0001.JPG", encodeTestJPEG(t, renderScene(wholeScene, 400, 300, 1)))

	w := httptest.NewRecorder()
	detectBlurHandler(w, httptest.NewRequest("GET", "/api/detect-blur", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET returned %d, want 405", w.Code)
	}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"malformed body", `not json`, http.StatusBadRequest},
		{"no directory", `{"files":["100_IMG_0001.JPG"]}`, http.StatusBadRequest},
		{"directory traversal", `{"directory":"../elsewhere","files":["100_IMG_0001.JPG"]}`, http.StatusBadRequest},
		{"thumbnail cache", `{"directory":".thumbnails","files":["100_IMG_0001.JPG"]}`, http.StatusBadRequest},
		{"missing directory", `{"directory":"2020-01-01_gone","files":["100_IMG_0001.JPG"]}`, http.StatusNotFound},
		{"no files", `{"directory":"` + directory + `","files":[]}`, http.StatusBadRequest},
		{"file traversal", `{"directory":"` + directory + `","files":["../100_IMG_0001.JPG"]}`, http.StatusBadRequest},
		{"hidden file", `{"directory":"` + directory + `","files":[".edits.json"]}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if w := postJSON(t, detectBlurHandler, "/api/detect-blur", tc.body); w.Code != tc.want {
			t.Errorf("%s: got %d, want %d (%s)", tc.name, w.Code, tc.want, strings.TrimSpace(w.Body.String()))
		}
	}
}
