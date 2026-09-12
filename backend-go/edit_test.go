package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gradientPNG encodes a w x h image whose red channel ramps left to right and
// whose green channel ramps top to bottom, so a crop is visible in the pixels.
func gradientPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x * 255 / lastIndex(w)),
				G: uint8(y * 255 / lastIndex(h)),
				B: 128,
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// lastIndex is the divisor that spreads a gradient across a span, guarding
// the single-pixel case.
func lastIndex(span int) int {
	if span > 1 {
		return span - 1
	}
	return 1
}

func decodeBytes(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding rendered image: %v", err)
	}
	return img
}

func TestBuildToneLUT(t *testing.T) {
	identity := buildToneLUT(toneParams{channelGain: 1})
	for i := range identity {
		if int(identity[i]) != i {
			t.Fatalf("neutral LUT changed %d to %d", i, identity[i])
		}
	}

	brighter := buildToneLUT(toneParams{channelGain: 1, exposure: 1})
	darker := buildToneLUT(toneParams{channelGain: 1, exposure: -1})
	if brighter[128] <= 128 {
		t.Errorf("+1 stop mapped 128 to %d, want brighter", brighter[128])
	}
	if darker[128] >= 128 {
		t.Errorf("-1 stop mapped 128 to %d, want darker", darker[128])
	}
	if brighter[0] != 0 || darker[0] != 0 {
		t.Errorf("exposure moved pure black: +1 -> %d, -1 -> %d", brighter[0], darker[0])
	}
	if brighter[255] != 255 {
		t.Errorf("+1 stop mapped white to %d, want 255", brighter[255])
	}

	crushed := buildToneLUT(toneParams{channelGain: 1, black: 100})
	lifted := buildToneLUT(toneParams{channelGain: 1, black: -100})
	if crushed[40] != 0 {
		t.Errorf("black level +100 mapped 40 to %d, want the shadows crushed to 0", crushed[40])
	}
	if lifted[0] == 0 {
		t.Errorf("black level -100 left pure black at 0, want it lifted")
	}
	if crushed[255] != 255 || lifted[255] != 255 {
		t.Errorf("black level moved white: +100 -> %d, -100 -> %d", crushed[255], lifted[255])
	}
}

// TestToneLUTReferenceValues pins the curve to the same table asserted in
// frontend/src/EditModal.test.js, so the on-canvas preview and the rendered
// file cannot drift apart.
func TestToneLUTReferenceValues(t *testing.T) {
	tests := []struct {
		params toneParams
		input  int
		want   uint8
	}{
		{toneParams{channelGain: 1, exposure: 1}, 128, 175},
		{toneParams{channelGain: 1, exposure: -1}, 128, 93},
		{toneParams{channelGain: 1, black: 50}, 128, 110},
		{toneParams{channelGain: 1, black: -50}, 0, 28},
		{toneParams{channelGain: 1, exposure: 0.5, black: 25}, 200, 233},
		{toneParams{channelGain: 1, exposure: 1}, 64, 88},
		// Highlight recovery: a bright value rolls down, and pure white with it.
		{toneParams{channelGain: 1, highlights: -100}, 200, 183},
		{toneParams{channelGain: 1, highlights: -100}, 255, 208},
		// The pair the bird case turns on — +1.5 stops clips 200 to white, and
		// the shoulder brings it back under with room to spare.
		{toneParams{channelGain: 1, exposure: 1.5}, 200, 255},
		{toneParams{channelGain: 1, exposure: 1.5, highlights: -80}, 200, 235},
		// A sky pull darkens what is already bright and ignores what is not.
		{toneParams{channelGain: 1, skyPull: 1}, 230, 184},
		{toneParams{channelGain: 1, skyPull: 2}, 230, 154},
		{toneParams{channelGain: 1, skyPull: 2}, 60, 60},
		// The shadow lift is the mirror: it moves a dark value a long way and
		// leaves a bright one exactly alone.
		{toneParams{channelGain: 1, shadows: 1}, 51, 88},
		{toneParams{channelGain: 1, shadows: 0.5}, 51, 69},
		{toneParams{channelGain: 1, shadows: 1}, 200, 200},
		// A white balance gain is a per-channel exposure, so it lands where the
		// same number of stops on the exposure slider would.
		{toneParams{channelGain: 2}, 128, 175},
		{toneParams{channelGain: 0.5}, 128, 93},
		// And everything at once, the way the sliders stack them.
		{toneParams{channelGain: 1, exposure: 1.5, highlights: -60, skyPull: 1.2}, 210, 222},
	}
	for _, tt := range tests {
		if got := buildToneLUT(tt.params)[tt.input]; got != tt.want {
			t.Errorf("buildToneLUT(%+v)[%d] = %d, want %d", tt.params, tt.input, got, tt.want)
		}
	}
}

// TestApplyHighlights covers the shoulder on its own: what it leaves alone,
// what it brings back under white, and that the two directions are inverses.
func TestApplyHighlights(t *testing.T) {
	// Nothing below the knee moves. At -100 the knee sits at 0.5.
	for _, v := range []float64{0, 0.25, 0.5} {
		if got := applyHighlights(v, -1); got != v {
			t.Errorf("applyHighlights(%v, -1) = %v, want it left alone below the knee", v, got)
		}
	}

	// The whole range an exposure lift can reach lands under white, still in
	// order, which is the detail that would otherwise have clipped. (The
	// shoulder only approaches white, so far enough out it rounds onto it.)
	prev := 0.0
	for _, v := range []float64{0.6, 0.8, 1.0, 1.5, 3.0, 6.0} {
		got := applyHighlights(v, -1)
		if got >= 1 {
			t.Errorf("applyHighlights(%v, -1) = %v, want it under white", v, got)
		}
		if got <= prev {
			t.Errorf("applyHighlights(%v, -1) = %v, want more than the %v below it", v, got, prev)
		}
		prev = got
	}

	// The shoulder leaves the knee at slope 1, so there is no kink there.
	const knee, eps = 0.5, 1e-6
	slope := (applyHighlights(knee+eps, -1) - knee) / eps
	if math.Abs(slope-1) > 1e-3 {
		t.Errorf("shoulder slope at the knee = %v, want 1", slope)
	}

	// Pushing highlights up is the exact inverse of rolling them off.
	for _, v := range []float64{0.55, 0.7, 0.9, 1.4} {
		rolled := applyHighlights(v, -0.6)
		if back := applyHighlights(rolled, 0.6); math.Abs(back-v) > 1e-9 {
			t.Errorf("highlights -60 then +60 turned %v into %v", v, back)
		}
	}

	if got := applyHighlights(0.9, 0); got != 0.9 {
		t.Errorf("applyHighlights(0.9, 0) = %v, want the value untouched", got)
	}
}

// TestHighlightRecoveryUnclipsTheSky is the bird case in LUT form: raising the
// exposure enough to see the bird flattens the top of the range to white, and
// the highlight slider has to put the steps back.
func TestHighlightRecoveryUnclipsTheSky(t *testing.T) {
	lifted := buildToneLUT(toneParams{channelGain: 1, exposure: 1.5})
	if lifted[190] != 255 || lifted[255] != 255 {
		t.Fatalf("+1.5 stops gave %d..%d for 190..255, want both clipped to white", lifted[190], lifted[255])
	}

	// The shoulder turns that plateau back into a ramp. It is a compressed one —
	// sixty-odd inputs sharing a dozen-odd 8-bit outputs — but a sky that rises
	// instead of sitting flat at white is the difference being asked for.
	recovered := buildToneLUT(toneParams{channelGain: 1, exposure: 1.5, highlights: -80})
	if recovered[255] >= 255 {
		t.Errorf("recovered white = %d, want it under 255 so the sky holds detail", recovered[255])
	}
	if recovered[255] <= recovered[190] {
		t.Errorf("recovered 190..255 spans %d..%d, want it still rising",
			recovered[190], recovered[255])
	}
	levels := map[uint8]bool{}
	for i := 190; i < 255; i++ {
		if recovered[i] > recovered[i+1] {
			t.Errorf("recovered LUT fell back at %d (%d -> %d), want the curve monotonic",
				i, recovered[i], recovered[i+1])
		}
		levels[recovered[i]] = true
	}
	if len(levels) < 8 {
		t.Errorf("recovered 190..255 onto %d distinct levels, want the sky's steps kept apart", len(levels))
	}
	// And the bird itself, down in the midtones, keeps the exposure it was given.
	if recovered[90] != lifted[90] {
		t.Errorf("recovery moved midtone 90 from %d to %d, want the bird left bright",
			lifted[90], recovered[90])
	}
}

func TestSkyGradient(t *testing.T) {
	const height = 200

	// Everything down to the horizon takes the pull in full; the feather band
	// below it eases out, and past that the ground is left alone.
	for _, y := range []int{0, 50, 99} {
		if got := skyGradient(y, height, 50); got != 1 {
			t.Errorf("gradient at row %d (above a 50%% horizon) = %v, want the full pull", y, got)
		}
	}
	if mid := skyGradient(125, height, 50); mid <= 0 || mid >= 1 {
		t.Errorf("gradient halfway through the feather = %v, want it part way out", mid)
	}
	for _, y := range []int{150, 180, height - 1} {
		if got := skyGradient(y, height, 50); got != 0 {
			t.Errorf("gradient at row %d (past the feather) = %v, want 0", y, got)
		}
	}

	// A horizon at the bottom edge pushes the whole feather off the frame, so
	// the pull is even everywhere — what a bird against nothing but sky needs.
	for _, y := range []int{0, 100, height - 1} {
		if got := skyGradient(y, height, 100); got != 1 {
			t.Errorf("gradient at row %d with the horizon at the bottom = %v, want the full pull", y, got)
		}
	}

	prev := 2.0
	for y := 0; y < height; y++ {
		got := skyGradient(y, height, 50)
		if got > prev {
			t.Fatalf("gradient rose again at row %d (%v after %v)", y, got, prev)
		}
		prev = got
	}

	if got := skyGradient(0, 0, 50); got != 0 {
		t.Errorf("gradient of an empty frame = %v, want 0", got)
	}
}

func TestSkyPullAndHorizonFromSlider(t *testing.T) {
	tests := []struct {
		edit    photoEdit
		pull    float64
		horizon float64
	}{
		{photoEdit{}, 0, defaultHorizon},
		{photoEdit{Sky: -20}, 0, defaultHorizon},
		{photoEdit{Sky: 50, Horizon: 30}, maxSkyPull / 2, 30},
		{photoEdit{Sky: 100, Horizon: 100}, maxSkyPull, 100},
		// An edit saved before the horizon control existed covers the whole frame.
		{photoEdit{Sky: 100}, maxSkyPull, 100},
		// A horizon too thin to fade across is widened rather than drawn as an edge.
		{photoEdit{Sky: 100, Horizon: 1}, maxSkyPull, minHorizon},
		{photoEdit{Sky: 200, Horizon: 400}, maxSkyPull, 100},
	}
	for _, tt := range tests {
		if got := tt.edit.skyPullStops(); got != tt.pull {
			t.Errorf("%+v.skyPullStops() = %v, want %v", tt.edit, got, tt.pull)
		}
		if got := tt.edit.horizonPercent(); got != tt.horizon {
			t.Errorf("%+v.horizonPercent() = %v, want %v", tt.edit, got, tt.horizon)
		}
	}

	// A sky pull is a real edit; a slider left at zero is not.
	if !(photoEdit{Sky: 0, Horizon: 40}).isIdentity() {
		t.Error("a horizon with no pull behind it should count as no edit")
	}
	if (photoEdit{Sky: 40}).isIdentity() || (photoEdit{Highlights: -40}).isIdentity() ||
		(photoEdit{Shadows: 40}).isIdentity() || (photoEdit{Temperature: 5}).isIdentity() ||
		(photoEdit{Tint: -5}).isIdentity() {
		t.Error("any of the tone or colour sliders should count as an edit")
	}
	if !(photoEdit{Shadows: -10}).isIdentity() {
		t.Error("the shadow slider only lifts, so a negative value is no edit")
	}
}

// skySceneGrey encodes the shape of a bird photo: bright sky across the top
// half, a dark bird in it, and darker ground below the horizon. Every pixel is
// grey, so one channel tells the whole story.
func skySceneGrey(t *testing.T, sky, bird, ground uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			v := sky
			if y >= 20 {
				v = ground
			} else if y >= 4 && y < 9 && x >= 16 && x < 25 {
				v = bird
			}
			img.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func greyAt(t *testing.T, img image.Image, x, y int) int {
	t.Helper()
	b := img.Bounds()
	r, _, _, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
	return int(r >> 8)
}

// TestRenderEditBalancesTheSky is the whole feature end to end: lift the
// exposure for the bird, then take the glare back off the sky without dragging
// the bird or the ground down with it.
func TestRenderEditBalancesTheSky(t *testing.T) {
	src := skySceneGrey(t, 228, 70, 96)

	// Lifting the exposure alone is the problem being fixed: the sky clips.
	lifted, err := renderEdit(src, photoEdit{Exposure: 1.5})
	if err != nil {
		t.Fatalf("renderEdit() error = %v", err)
	}
	if got := greyAt(t, decodeBytes(t, lifted), 2, 2); got != 255 {
		t.Fatalf("sky after +1.5 stops = %d, want it blown to 255 for the test to mean anything", got)
	}

	balanced, err := renderEdit(src, photoEdit{Exposure: 1.5, Highlights: -60, Sky: 80, Horizon: 50})
	if err != nil {
		t.Fatalf("renderEdit() error = %v", err)
	}
	out := decodeBytes(t, balanced)

	if sky := greyAt(t, out, 2, 2); sky >= 250 || sky <= 128 {
		t.Errorf("balanced sky = %d, want it off the clipping point but still sky", sky)
	}
	// The bird keeps very nearly the exposure it was lifted to: it sits at the
	// bottom of the sky pull's knee, so it takes a sliver of the pull where the
	// sky above it takes all of it. Shadows is the slider with the bit-exact
	// promise; this one only has to leave the bird where the eye left it.
	bird := greyAt(t, out, 20, 6)
	birdLifted := greyAt(t, decodeBytes(t, lifted), 20, 6)
	if d := birdLifted - bird; d < 0 || d > 8 {
		t.Errorf("bird = %d against the %d the exposure lift gave it, want it within a few levels",
			bird, birdLifted)
	}
	// Which only means anything next to what the sky gave up.
	skyGaveUp := greyAt(t, decodeBytes(t, lifted), 2, 2) - greyAt(t, out, 2, 2)
	if skyGaveUp < 4*(birdLifted-bird) {
		t.Errorf("the sky gave up %d and the bird %d, want the pull landing on the sky",
			skyGaveUp, birdLifted-bird)
	}
	if bird <= 70 {
		t.Errorf("bird = %d, want it brighter than the %d it started at", bird, 70)
	}
	// And the ground well below the horizon is outside the gradient entirely.
	ground := greyAt(t, out, 20, 34)
	groundLifted := greyAt(t, decodeBytes(t, lifted), 20, 34)
	if ground != groundLifted {
		t.Errorf("ground = %d, want the %d it had past the feather", ground, groundLifted)
	}

	// The sky above the horizon is pulled evenly — the gradient is there to keep
	// the pull off the ground, not to shade the sky itself.
	if top, low := greyAt(t, out, 2, 1), greyAt(t, out, 2, 18); top != low {
		t.Errorf("sky at the top = %d and just above the horizon = %d, want them even", top, low)
	}

	// Between the two the pull eases away rather than stopping at a line, so the
	// ground gets steadily lighter across the feather band.
	prev := -1
	for y := 20; y <= 34; y++ {
		got := greyAt(t, out, 20, y)
		if got < prev {
			t.Fatalf("ground at row %d = %d, want it no darker than the %d above it", y, got, prev)
		}
		prev = got
	}
	if first, last := greyAt(t, out, 20, 21), greyAt(t, out, 20, 34); first >= last {
		t.Errorf("ground runs %d..%d across the feather, want the pull easing off", first, last)
	}
}

// TestRenderEditGradesBeforeCropping pins the gradient to the whole frame, so
// moving a crop around does not drag the sky pull along with it.
func TestRenderEditGradesBeforeCropping(t *testing.T) {
	src := skySceneGrey(t, 228, 70, 228) // bright top and bottom, so only the grade shows
	edit := photoEdit{Sky: 100, Horizon: 50}

	full, err := renderEdit(src, edit)
	if err != nil {
		t.Fatalf("renderEdit() error = %v", err)
	}
	wholeFrame := decodeBytes(t, full)

	edit.Crop = &cropRect{X: 0, Y: 0.5, W: 1, H: 0.5}
	cropped, err := renderEdit(src, edit)
	if err != nil {
		t.Fatalf("renderEdit() error = %v", err)
	}
	out := decodeBytes(t, cropped)

	// Row n of a bottom-half crop is row 20+n of the frame and has to carry that
	// row's share of the gradient. Re-anchoring to the crop would start the pull
	// over at full strength here, darkening rows the horizon had already let go.
	for _, n := range []int{0, 5, 10, 15} {
		if got, want := greyAt(t, out, 2, n), greyAt(t, wholeFrame, 2, 20+n); got != want {
			t.Errorf("row %d of a bottom-half crop = %d, want row %d of the whole frame (%d)",
				n, got, 20+n, want)
		}
	}
	// The sanity check on that: the gradient really has run out by then.
	if got := greyAt(t, wholeFrame, 2, 35); got != greyAt(t, decodeBytes(t, skySceneGrey(t, 228, 70, 228)), 2, 35) {
		t.Errorf("row 35 of the whole frame = %d, want it left ungraded past the feather", got)
	}
}

func TestRenderEditCrops(t *testing.T) {
	src := gradientPNG(t, 100, 50)

	out, err := renderEdit(src, photoEdit{Crop: &cropRect{X: 0.5, Y: 0, W: 0.5, H: 0.5}})
	if err != nil {
		t.Fatalf("renderEdit() error = %v", err)
	}
	img := decodeBytes(t, out)
	if got := img.Bounds().Dx(); got != 50 {
		t.Errorf("cropped width = %d, want 50", got)
	}
	if got := img.Bounds().Dy(); got != 25 {
		t.Errorf("cropped height = %d, want 25", got)
	}
	// The crop starts halfway across, so its first column is mid-ramp, not black.
	r, _, _, _ := img.At(0, 0).RGBA()
	if r>>8 < 120 || r>>8 > 135 {
		t.Errorf("top-left red of the right-hand crop = %d, want the middle of the ramp", r>>8)
	}
}

func TestRenderEditAdjustsTone(t *testing.T) {
	src := gradientPNG(t, 20, 20)

	brighter, err := renderEdit(src, photoEdit{Exposure: 1})
	if err != nil {
		t.Fatalf("renderEdit() error = %v", err)
	}
	base := decodeBytes(t, src)
	lifted := decodeBytes(t, brighter)

	br, _, _, _ := base.At(10, 10).RGBA()
	lr, _, _, _ := lifted.At(10, 10).RGBA()
	if lr <= br {
		t.Errorf("exposure +1 gave red %d, want brighter than %d", lr>>8, br>>8)
	}

	crushed, err := renderEdit(src, photoEdit{Black: 100})
	if err != nil {
		t.Fatalf("renderEdit() error = %v", err)
	}
	cr, _, _, _ := decodeBytes(t, crushed).At(1, 1).RGBA()
	if cr != 0 {
		t.Errorf("black level +100 gave shadow red %d, want 0", cr>>8)
	}
}

func TestRenderEditKeepsExifAndResetsOrientation(t *testing.T) {
	src := jpegWithOrientation(t, 40, 20, 6)

	out, err := renderEdit(src, photoEdit{Exposure: 0.5})
	if err != nil {
		t.Fatalf("renderEdit() error = %v", err)
	}

	// Orientation 6 is "rotate 90 CW", which the render bakes into the pixels.
	img := decodeBytes(t, out)
	if img.Bounds().Dx() != 20 || img.Bounds().Dy() != 40 {
		t.Errorf("rendered size = %dx%d, want 20x40 (rotated)", img.Bounds().Dx(), img.Bounds().Dy())
	}

	meta, err := extractPhotoMetadata(writeTemp(t, out))
	if err != nil {
		t.Fatalf("edited photo lost its EXIF: %v", err)
	}
	if meta.ShutterSpeed != "1/250s" || meta.ISO != "ISO 400" {
		t.Errorf("edited photo EXIF = %+v, want the camera settings carried over", meta)
	}
	if meta.Orientation != 1 {
		t.Errorf("edited photo orientation = %d, want 1 so viewers don't rotate it twice", meta.Orientation)
	}
}

func TestApplyOrientation(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	src.Set(0, 0, color.RGBA{R: 255, A: 255}) // mark the top-left corner

	rotated := applyOrientation(src, 6) // 90 CW: top-left moves to top-right
	if rotated.Bounds().Dx() != 2 || rotated.Bounds().Dy() != 4 {
		t.Fatalf("rotated size = %v, want 2x4", rotated.Bounds())
	}
	if r, _, _, _ := rotated.At(1, 0).RGBA(); r>>8 != 255 {
		t.Errorf("marked corner did not land top-right after a 90 CW rotation")
	}

	if got := applyOrientation(src, 1); got != image.Image(src) {
		t.Errorf("orientation 1 should return the image untouched")
	}
}

func TestCropRectValidation(t *testing.T) {
	tests := []struct {
		name string
		crop cropRect
		want bool
	}{
		{"full frame", cropRect{0, 0, 1, 1}, true},
		{"half", cropRect{0.25, 0.25, 0.5, 0.5}, true},
		{"rounding slack", cropRect{0, 0, 1.0000001, 1}, true},
		{"zero size", cropRect{0, 0, 0, 0}, false},
		{"too small", cropRect{0, 0, 0.001, 0.5}, false},
		{"off the right edge", cropRect{0.6, 0, 0.5, 0.5}, false},
		{"negative origin", cropRect{-0.2, 0, 0.5, 0.5}, false},
	}
	for _, tt := range tests {
		if got := tt.crop.valid(); got != tt.want {
			t.Errorf("%s: cropRect%+v.valid() = %v, want %v", tt.name, tt.crop, got, tt.want)
		}
	}
}

func TestIsEditableImage(t *testing.T) {
	tests := map[string]bool{
		"IMG_0001.JPG":  true,
		"IMG_0001.jpeg": true,
		"shot.png":      true,
		"IMG_0001.CR3":  false,
		"IMG_0001.ORF":  false,
		"clip.MP4":      false,
	}
	for name, want := range tests {
		if got := isEditableImage(name); got != want {
			t.Errorf("isEditableImage(%q) = %v, want %v", name, got, want)
		}
	}
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.jpg")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// jpegWithOrientation encodes a JPEG carrying the shared test EXIF block plus
// the given orientation tag.
func jpegWithOrientation(t *testing.T, w, h, orientation int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 100, G: 100, B: 100, A: 255})
		}
	}
	var body bytes.Buffer
	if err := jpeg.Encode(&body, img, nil); err != nil {
		t.Fatal(err)
	}

	tiff := buildTestExifTIFFWithOrientation(orientation)
	payloadLen := 2 + 6 + len(tiff)
	segment := []byte{0xFF, 0xE1, byte(payloadLen >> 8), byte(payloadLen)}
	segment = append(segment, []byte("Exif\x00\x00")...)
	segment = append(segment, tiff...)
	return jpegInsertExif(body.Bytes(), segment)
}

// buildTestExifTIFFWithOrientation extends the shared fixture with an
// Orientation entry in IFD0.
func buildTestExifTIFFWithOrientation(orientation int) []byte {
	base := buildTestExifTIFF()
	buf := make([]byte, len(base)+12)
	le := binary.LittleEndian
	copy(buf, base[:8])

	// IFD0 grows from one entry to two; everything it points at lives beyond
	// offset 26 in the fixture, so shift those offsets by one entry's width.
	le.PutUint16(buf[8:], 2)
	le.PutUint16(buf[10:], 0x0112) // Orientation
	le.PutUint16(buf[12:], 3)      // SHORT
	le.PutUint32(buf[14:], 1)
	le.PutUint32(buf[18:], uint32(orientation))
	le.PutUint16(buf[22:], 0x8769) // Exif SubIFD pointer
	le.PutUint16(buf[24:], 4)      // LONG
	le.PutUint32(buf[26:], 1)
	le.PutUint32(buf[30:], 26+12)
	le.PutUint32(buf[34:], 0) // no next IFD

	// Copy the SubIFD and its rationals across, fixing up the value offsets.
	copy(buf[38:], base[26:])
	for i := 0; i < 4; i++ {
		e := 40 + i*12
		if le.Uint16(buf[e+2:]) == 5 { // RATIONAL values are referenced by offset
			le.PutUint32(buf[e+8:], le.Uint32(buf[e+8:])+12)
		}
	}
	return buf
}

func TestBuildTestExifTIFFWithOrientation(t *testing.T) {
	meta, err := parseExifTIFF(buildTestExifTIFFWithOrientation(6))
	if err != nil {
		t.Fatalf("parseExifTIFF() error = %v", err)
	}
	want := photoMetadata{ShutterSpeed: "1/250s", Aperture: "f/5.6", ISO: "ISO 400", FocalLength: "50mm", Orientation: 6}
	if meta != want {
		t.Errorf("parseExifTIFF() = %+v, want %+v", meta, want)
	}
}

// withTestSession points photoBaseDir at a temp directory holding one photo
// and returns the session name.
func withTestSession(t *testing.T, name string, contents []byte) string {
	t.Helper()
	photoBaseDir = t.TempDir()
	thumbnailCacheDir = filepath.Join(photoBaseDir, ".thumbnails")
	directory := "2026-01-01_batch"
	dir := filepath.Join(photoBaseDir, directory)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), contents, 0644); err != nil {
		t.Fatal(err)
	}
	return directory
}

func postJSON(t *testing.T, handler http.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return w
}

func TestEditPhotoHandlerBacksUpOriginalAndRecordsEdit(t *testing.T) {
	original := gradientPNG(t, 60, 40)
	directory := withTestSession(t, "100_IMG_0001.png", original)

	w := postJSON(t, editPhotoHandler, "/api/edit-photo",
		`{"directory":"`+directory+`","photo":"100_IMG_0001.png","crop":{"x":0,"y":0,"w":0.5,"h":1},`+
			`"exposure":0.5,"black":10,"highlights":-40,"shadows":55,"sky":65,"horizon":35,`+
			`"temperature":12,"tint":-8}`)
	if w.Code != http.StatusOK {
		t.Fatalf("edit returned %d, want 200: %s", w.Code, w.Body.String())
	}

	// The pristine original is byte-for-byte in unedited/.
	backup, err := os.ReadFile(filepath.Join(photoBaseDir, directory, uneditedDirName, "100_IMG_0001.png"))
	if err != nil {
		t.Fatalf("original was not backed up: %v", err)
	}
	if !bytes.Equal(backup, original) {
		t.Error("backed-up original does not match the imported file")
	}

	// The photo itself now carries the crop.
	edited, err := os.ReadFile(filepath.Join(photoBaseDir, directory, "100_IMG_0001.png"))
	if err != nil {
		t.Fatal(err)
	}
	if img := decodeBytes(t, edited); img.Bounds().Dx() != 30 || img.Bounds().Dy() != 40 {
		t.Errorf("edited photo is %v, want a 30x40 crop", img.Bounds())
	}

	// And the settings are recorded so the editor can reopen with them.
	var record map[string]photoEdit
	raw, err := os.ReadFile(filepath.Join(photoBaseDir, directory, editsFileName))
	if err != nil {
		t.Fatalf("no edit record written: %v", err)
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	got := record["100_IMG_0001.png"]
	if got.Exposure != 0.5 || got.Black != 10 || got.Crop == nil || got.Crop.W != 0.5 {
		t.Errorf("edit record = %+v, want the submitted adjustments", got)
	}
	if got.Highlights != -40 || got.Sky != 65 || got.Horizon != 35 || got.Shadows != 55 {
		t.Errorf("edit record = %+v, want the tone settings recorded too", got)
	}
	if got.Temperature != 12 || got.Tint != -8 {
		t.Errorf("edit record = %+v, want the white balance recorded too", got)
	}
	if got.EditedAt == "" {
		t.Error("edit record has no timestamp")
	}
}

func TestEditPhotoHandlerReeditsFromTheOriginal(t *testing.T) {
	original := gradientPNG(t, 60, 40)
	directory := withTestSession(t, "shot.png", original)
	body := `{"directory":"` + directory + `","photo":"shot.png","crop":{"x":0,"y":0,"w":0.5,"h":1},"exposure":0,"black":0}`

	if w := postJSON(t, editPhotoHandler, "/api/edit-photo", body); w.Code != http.StatusOK {
		t.Fatalf("first edit returned %d: %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, editPhotoHandler, "/api/edit-photo", body); w.Code != http.StatusOK {
		t.Fatalf("second edit returned %d: %s", w.Code, w.Body.String())
	}

	// Applying the same 50% crop twice must not compound into 25%.
	edited, err := os.ReadFile(filepath.Join(photoBaseDir, directory, "shot.png"))
	if err != nil {
		t.Fatal(err)
	}
	if img := decodeBytes(t, edited); img.Bounds().Dx() != 30 {
		t.Errorf("re-edited width = %d, want 30 (edits apply to the original, not the last render)", img.Bounds().Dx())
	}
}

func TestEditPhotoHandlerUpdatesSelectedCopy(t *testing.T) {
	original := gradientPNG(t, 60, 40)
	directory := withTestSession(t, "shot.png", original)
	selectedDir := filepath.Join(photoBaseDir, directory, "selected")
	if err := os.MkdirAll(selectedDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selectedDir, "shot.png"), original, 0644); err != nil {
		t.Fatal(err)
	}

	w := postJSON(t, editPhotoHandler, "/api/edit-photo",
		`{"directory":"`+directory+`","photo":"shot.png","crop":{"x":0,"y":0,"w":0.5,"h":1}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("edit returned %d: %s", w.Code, w.Body.String())
	}

	saved, err := os.ReadFile(filepath.Join(selectedDir, "shot.png"))
	if err != nil {
		t.Fatal(err)
	}
	if img := decodeBytes(t, saved); img.Bounds().Dx() != 30 {
		t.Errorf("saved copy is %d wide, want the edited 30 so exports aren't stale", img.Bounds().Dx())
	}
}

func TestRevertPhotoHandlerRestoresTheOriginal(t *testing.T) {
	original := gradientPNG(t, 60, 40)
	directory := withTestSession(t, "shot.png", original)

	if w := postJSON(t, editPhotoHandler, "/api/edit-photo",
		`{"directory":"`+directory+`","photo":"shot.png","exposure":1.5}`); w.Code != http.StatusOK {
		t.Fatalf("edit returned %d: %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, revertPhotoHandler, "/api/revert-photo",
		`{"directory":"`+directory+`","photo":"shot.png"}`); w.Code != http.StatusOK {
		t.Fatalf("revert returned %d: %s", w.Code, w.Body.String())
	}

	restored, err := os.ReadFile(filepath.Join(photoBaseDir, directory, "shot.png"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Error("reverted photo does not match the original bytes")
	}
	if _, err := os.Stat(filepath.Join(photoBaseDir, directory, uneditedDirName)); !os.IsNotExist(err) {
		t.Error("unedited/ should be cleaned up once its last backup is reverted")
	}
	if _, err := os.Stat(filepath.Join(photoBaseDir, directory, editsFileName)); !os.IsNotExist(err) {
		t.Error("edit record should be removed once nothing is edited")
	}
}

func TestEditPhotoHandlerWithNeutralSettingsReverts(t *testing.T) {
	original := gradientPNG(t, 30, 30)
	directory := withTestSession(t, "shot.png", original)

	if w := postJSON(t, editPhotoHandler, "/api/edit-photo",
		`{"directory":"`+directory+`","photo":"shot.png","exposure":1}`); w.Code != http.StatusOK {
		t.Fatalf("edit returned %d: %s", w.Code, w.Body.String())
	}
	w := postJSON(t, editPhotoHandler, "/api/edit-photo",
		`{"directory":"`+directory+`","photo":"shot.png","exposure":0,"black":0,"crop":{"x":0,"y":0,"w":1,"h":1}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("neutral edit returned %d: %s", w.Code, w.Body.String())
	}

	restored, err := os.ReadFile(filepath.Join(photoBaseDir, directory, "shot.png"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Error("zeroing every slider should put the original back rather than re-encode it")
	}
}

func TestEditPhotoHandlerRejectsBadRequests(t *testing.T) {
	directory := withTestSession(t, "shot.png", gradientPNG(t, 20, 20))

	tests := []struct {
		name string
		body string
	}{
		{"raw file", `{"directory":"` + directory + `","photo":"IMG_0001.CR3","exposure":1}`},
		{"path traversal", `{"directory":"` + directory + `","photo":"../escape.png","exposure":1}`},
		{"missing photo field", `{"directory":"` + directory + `"}`},
		{"crop off the edge", `{"directory":"` + directory + `","photo":"shot.png","crop":{"x":0.9,"y":0,"w":0.5,"h":0.5}}`},
		{"exposure out of range", `{"directory":"` + directory + `","photo":"shot.png","exposure":50}`},
		{"highlights out of range", `{"directory":"` + directory + `","photo":"shot.png","highlights":160}`},
		{"sky below zero", `{"directory":"` + directory + `","photo":"shot.png","sky":-10}`},
		{"horizon past the frame", `{"directory":"` + directory + `","photo":"shot.png","sky":50,"horizon":140}`},
		{"shadows below zero", `{"directory":"` + directory + `","photo":"shot.png","shadows":-20}`},
		{"shadows out of range", `{"directory":"` + directory + `","photo":"shot.png","shadows":250}`},
		{"temperature out of range", `{"directory":"` + directory + `","photo":"shot.png","temperature":-300}`},
		{"tint out of range", `{"directory":"` + directory + `","photo":"shot.png","tint":120}`},
	}
	for _, tt := range tests {
		if w := postJSON(t, editPhotoHandler, "/api/edit-photo", tt.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400: %s", tt.name, w.Code, w.Body.String())
		}
	}
}

func TestEditsHandlerReportsBackupsWithoutRecords(t *testing.T) {
	directory := withTestSession(t, "shot.png", gradientPNG(t, 20, 20))
	if err := os.MkdirAll(filepath.Join(photoBaseDir, directory, uneditedDirName), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(photoBaseDir, directory, uneditedDirName, "orphan.png"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	editsHandler(w, httptest.NewRequest("GET", "/api/edits?directory="+directory, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("edits returned %d: %s", w.Code, w.Body.String())
	}
	var edits map[string]photoEdit
	if err := json.Unmarshal(w.Body.Bytes(), &edits); err != nil {
		t.Fatal(err)
	}
	if _, ok := edits["orphan.png"]; !ok {
		t.Errorf("edits = %v, want a photo with a backup reported as edited", edits)
	}
}
