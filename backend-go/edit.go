package main

// Non-destructive photo editing: crop, white balance, exposure, black level,
// highlight recovery, shadow lift and a graduated sky pull.
//
// The pristine original is copied into an "unedited" subfolder of the session
// the first time a photo is edited, and every render starts from that copy, so
// adjustments never stack on top of each other and the original can always be
// restored. The parameters behind the current render live in a per-session
// ".edits.json" sidecar so the editor can reopen with the sliders where the
// user left them.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	uneditedDirName = "unedited"
	editsFileName   = ".edits.json"

	// Re-encode quality for edited JPEGs. High enough that a round trip is
	// visually lossless; the original is kept untouched in unedited/ anyway.
	editJPEGQuality = 92

	// Display gamma used to move between sRGB values and linear light.
	displayGamma = 2.2

	// A black level of ±100 moves the black point by this fraction of the
	// tonal range — enough to crush or lift shadows without destroying them.
	maxBlackShift = 0.25

	// At highlights = ±100 the shoulder takes over this far down the display
	// range, so everything above the halfway point is rolled off (or stretched).
	maxHighlightKnee = 0.5

	// Stops of exposure the sky slider takes off the top of the frame at full
	// strength. Two is enough to bring a white sky back to a readable blue.
	maxSkyPull = 2.0

	// Linear-light level the sky pull compresses towards — middle grey in display
	// terms (0.45^displayGamma). Deep shadows come through untouched and a
	// midtone very nearly so, which is what keeps a bird roughly where the
	// exposure slider put it while the sky behind it comes down. It is a soft
	// promise, not the bit-exact one applyShadows makes; and a bird as pale as
	// the sky it is flying against cannot be told apart this way at all, where
	// the gradient is the only thing limiting the pull.
	skyPivot = 0.1762

	// Half-width of the sky pull's soft knee, as a fraction of the pivot. The
	// slope change eases in across it rather than arriving as a contour line.
	skyKnee = 0.9

	// How far up the display range the shadow lift reaches, and what it adds to
	// the slope at black at full strength. The knee is fixed and only the amount
	// follows the slider, so the slider's travel maps evenly onto how much a
	// given dark pixel moves. It sits at or below every highlight knee, so the
	// two curves can meet but never overlap and their order does not matter.
	// The slope stays positive for any lift below 3, so 2 leaves room: the curve
	// can never double back on itself.
	maxShadowKnee = 0.5
	maxShadowLift = 2.0

	// Stops each white balance slider moves its channels at ±100. A stop each
	// way is far more than an outdoor cast needs — the sample below wants about
	// a tenth of one — but it means the grey-point picker is never clamped.
	maxWhiteBalance = 1.0

	// How far down the frame the sky pull reaches, as a percentage of frame
	// height, when a record carries a pull but no horizon of its own. A bird is
	// as often framed against sky top to bottom as over a landscape, so the
	// default covers the whole frame and leaves the brightness mask to decide.
	defaultHorizon = 100.0

	// How far below the horizon the pull takes to ease away, as a fraction of
	// frame height. Wide enough that a lowered horizon reads as haze clearing
	// rather than as a line drawn across the photo — and it is why a horizon at
	// 100% covers the frame evenly: the whole fade falls off the bottom edge.
	skyFeather = 0.25

	// The pull needs some frame to cover; any less is indistinguishable from off.
	minHorizon = 5.0

	// Crops smaller than this fraction of a side are rejected as accidental.
	minCropFraction = 0.01
)

// cropRect is a crop expressed in fractions of the original image, measured
// after EXIF orientation has been applied (i.e. relative to the photo as the
// browser draws it). Storing fractions rather than pixels keeps a saved crop
// meaningful regardless of the source resolution.
type cropRect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// photoEdit is the full set of adjustments applied to one photo.
type photoEdit struct {
	Crop        *cropRect `json:"crop,omitempty"`
	Temperature float64   `json:"temperature"` // -100 cools the photo, +100 warms it
	Tint        float64   `json:"tint"`        // -100 towards green, +100 towards magenta
	Exposure    float64   `json:"exposure"`    // stops, negative darkens
	Black       float64   `json:"black"`       // -100 lifts shadows, +100 crushes them
	Highlights  float64   `json:"highlights"`  // -100 rolls highlights off, +100 pushes them up
	Shadows     float64   `json:"shadows"`     // 0-100, how far the shadows are opened up
	Sky         float64   `json:"sky"`         // 0-100, how hard the top of the frame is pulled down
	Horizon     float64   `json:"horizon"`     // 0-100, how far down the frame the sky pull reaches
	EditedAt    string    `json:"edited_at,omitempty"`
}

// isIdentity reports whether the edit would leave the photo unchanged, in
// which case applying it is treated as a revert instead of a re-encode.
func (e photoEdit) isIdentity() bool {
	if e.Exposure != 0 || e.Black != 0 || e.Highlights != 0 ||
		e.Temperature != 0 || e.Tint != 0 ||
		e.shadowLift() != 0 || e.skyPullStops() != 0 {
		return false
	}
	return e.Crop == nil || e.Crop.isFull()
}

// shadowLift is the shadow slider as the fraction the curve works in. Like the
// sky pull it only goes one way: deepening the shadows is what the black point
// is for, and giving one job to two sliders only makes them harder to predict.
func (e photoEdit) shadowLift() float64 {
	if e.Shadows <= 0 {
		return 0
	}
	return math.Min(e.Shadows, 100) / 100
}

// skyPullStops is how many stops of exposure the sky slider takes off the top
// of the frame. The slider only ever darkens, so anything at or below zero is
// no pull at all — brightening the sky is what the exposure slider is for.
func (e photoEdit) skyPullStops() float64 {
	if e.Sky <= 0 {
		return 0
	}
	return math.Min(e.Sky, 100) / 100 * maxSkyPull
}

// horizonPercent is how far down the frame the sky gradient reaches. An edit
// recorded before this control existed carries no horizon, so it falls back to
// the middle of the frame rather than to "no sky at all".
func (e photoEdit) horizonPercent() float64 {
	if e.Horizon <= 0 {
		return defaultHorizon
	}
	return math.Max(minHorizon, math.Min(100, e.Horizon))
}

func (c cropRect) isFull() bool {
	return c.X <= 0 && c.Y <= 0 && c.W >= 1 && c.H >= 1
}

func (c cropRect) valid() bool {
	const slack = 1e-6 // absorbs rounding in the fractions the browser sends
	return c.W >= minCropFraction && c.H >= minCropFraction &&
		c.X >= -slack && c.Y >= -slack &&
		c.X+c.W <= 1+slack && c.Y+c.H <= 1+slack
}

// editsMu serializes the read-modify-write cycle on a session's sidecar so
// two concurrent edits in the same folder can't lose each other's record.
var editsMu sync.Mutex

// isEditableImage reports whether the pipeline can decode, adjust and re-encode
// a file. RAW files are excluded: only their embedded JPEG preview is readable,
// so an "edit" would silently throw the raw sensor data away.
func isEditableImage(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".png")
}

// validPhotoName reports whether name is a plain filename, with no directory
// component that could point the edit at another folder.
func validPhotoName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\`) && !strings.HasPrefix(name, ".")
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// smoothstep ramps from 0 at lo to 1 at hi, flat at both ends, so a gradient
// or mask built out of it has no visible seam where it starts or stops.
func smoothstep(lo, hi, v float64) float64 {
	if hi <= lo {
		return 0
	}
	t := clamp01((v - lo) / (hi - lo))
	return t * t * (3 - 2*t)
}

// applyHighlights bends the top of the display range, leaving everything below
// the knee exactly where it was. h is the slider over ±1.
//
// A negative h is highlight recovery: the shoulder k + s*(1 - e^-t) squeezes
// the whole open-ended range above the knee into the gap left below white, so a
// sky that an exposure lift pushed past 1.0 lands just under it with its
// gradients intact instead of clipping flat. It leaves the knee with slope 1,
// so there is no kink where it takes over. A positive h is its exact inverse,
// stretching the highlights back up towards (and through) white.
func applyHighlights(v, h float64) float64 {
	if h == 0 || v <= 0 {
		return v
	}
	knee := 1 - math.Abs(h)*maxHighlightKnee
	if v <= knee {
		return v
	}
	span := 1 - knee // non-zero: |h| <= 1 and maxHighlightKnee < 1
	t := (v - knee) / span
	if h < 0 {
		return knee + span*(1-math.Exp(-t))
	}
	if t >= 1 {
		return 1 + span // past white either way; the caller clamps
	}
	return knee - span*math.Log(1-t)
}

// applySkyPull scales linear light above a pivot down by `stops`, leaving
// everything below the pivot exactly where it was, with the slope change eased
// in over a soft knee so it never draws a contour across a smooth sky.
//
// Scaling the region above the pivot, rather than weighting a multiply by how
// bright the pixel is, is what makes the curve monotone at every strength: the
// slope only ever moves between 2^-stops and 1, both positive, so a brighter
// pixel can never come out darker than a dimmer one. A brightness-weighted
// multiply cannot promise that — past about 1.2 stops its gain falls faster
// than the value rises and the sky's own gradient comes out inverted.
func applySkyPull(l, stops float64) float64 {
	if stops <= 0 {
		return l
	}
	gain := math.Pow(2, -stops)
	width := skyKnee * skyPivot
	x := (l - skyPivot) / width
	if x <= -1 {
		return l
	}
	if x >= 1 {
		return skyPivot + (l-skyPivot)*gain
	}
	// Across the knee the slope ramps from 1 to gain; this is that ramp's
	// integral, so the curve stays continuous and its slope never reaches zero.
	return skyPivot + width*(x+(gain-1)*(x+1)*(x+1)/4)
}

// applyShadows opens up the bottom of the display range, leaving everything
// above the knee exactly where it was. s is the slider over 0..1.
//
// The curve pins both ends — black stays black and the knee stays put — so it
// brightens what is dark without the milky wash that lifting the black point
// would give, and the sky, which lives far above the knee, is untouched by
// construction rather than by a mask that might let some through. It meets the
// knee at slope 1, so there is no kink where it takes over, and its slope stays
// positive for any lift under 3.
func applyShadows(v, s float64) float64 {
	if s <= 0 || v <= 0 {
		return v
	}
	if v >= maxShadowKnee {
		return v
	}
	t := v / maxShadowKnee
	return maxShadowKnee * (t + s*maxShadowLift*t*(1-t)*(1-t))
}

// whiteBalanceGains turns the temperature and tint sliders into the linear-light
// gain each colour channel takes. Temperature trades red against blue, the way
// the light's colour actually shifts between overcast and evening; tint moves
// green against the other two, which is the axis left over.
func whiteBalanceGains(temperature, tint float64) (r, g, b float64) {
	t := math.Max(-1, math.Min(1, temperature/100)) * maxWhiteBalance
	m := math.Max(-1, math.Min(1, tint/100)) * maxWhiteBalance
	return math.Pow(2, t), math.Pow(2, -m), math.Pow(2, -t)
}

// skyGradient is the share of the sky pull that row y of an h-row frame takes:
// the whole frame down to the horizon, then easing away over the feather band
// below it so the ground keeps the exposure it was given. With the horizon at
// the bottom edge the feather falls off the frame entirely and every row takes
// the pull in full, which is what a bird against nothing but sky wants.
//
// y is measured in the whole oriented frame rather than in the crop, so the
// gradient stays where the editor drew it however the crop is moved around.
func skyGradient(y, height int, horizon float64) float64 {
	if height <= 0 {
		return 0
	}
	edge := clamp01(horizon / 100)
	return 1 - smoothstep(edge, edge+skyFeather, (float64(y)+0.5)/float64(height))
}

// toneParams is one channel's worth of the tone pass: everything a single LUT
// needs to bake in. skyPull is the row's share of the graduated pull, in stops,
// and channelGain is that channel's white balance gain — the only field that
// differs between the three LUTs a row is rendered with.
type toneParams struct {
	channelGain float64
	exposure    float64
	skyPull     float64
	highlights  float64
	shadows     float64
	black       float64
}

// buildToneLUT maps every 8-bit channel value through the white balance gain,
// the exposure shift, the sky pull, the highlight roll-off, the shadow lift and
// finally the black-point move.
//
// White balance, exposure and the sky pull are applied in linear light (hence
// the gamma round trip) so they behave like changing the light or the aperture
// rather than washing the frame out evenly. Highlights, shadows and the black
// point then work in display space, where they match what the eye reads as
// rolled-off highlights, open shadows and deeper blacks: v' = (v - b) / (1 - b)
// pulls b down to zero and stretches what is left.
//
// Highlights and shadows bend opposite ends of the range and their knees can
// meet but never overlap, so the order between them does not matter.
//
// Nothing is clamped until the very end, which is what lets the highlight
// shoulder pull a value the exposure lift pushed past white back under it.
func buildToneLUT(p toneParams) [256]uint8 {
	gain := math.Pow(2, p.exposure) * p.channelGain
	h := math.Max(-1, math.Min(1, p.highlights/100))
	b := math.Max(-1, math.Min(1, p.black/100)) * maxBlackShift

	var lut [256]uint8
	for i := range lut {
		v := applySkyPull(math.Pow(float64(i)/255, displayGamma)*gain, p.skyPull)
		v = applyShadows(applyHighlights(math.Pow(v, 1/displayGamma), h), p.shadows)
		v = (v - b) / (1 - b)
		lut[i] = uint8(math.Round(clamp01(v) * 255))
	}
	return lut
}

// channelParams builds the three LUT parameter sets a row is rendered with: the
// same curve throughout, differing only in the white balance gain.
func (e photoEdit) channelParams(skyPull float64) (r, g, b toneParams) {
	rGain, gGain, bGain := whiteBalanceGains(e.Temperature, e.Tint)
	base := toneParams{
		exposure:   e.Exposure,
		skyPull:    skyPull,
		highlights: e.Highlights,
		shadows:    e.shadowLift(),
		black:      e.Black,
	}
	r, g, b = base, base, base
	r.channelGain, g.channelGain, b.channelGain = rGain, gGain, bGain
	return r, g, b
}

// toRGBA returns src as an *image.RGBA anchored at the origin, reusing the
// backing array when src is already in that form.
func toRGBA(src image.Image) *image.RGBA {
	if rgba, ok := src.(*image.RGBA); ok && rgba.Bounds().Min == (image.Point{}) {
		return rgba
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// rowLUTs is the trio of LUTs one row of pixels is pushed through — one per
// colour channel, so white balance can move them against each other.
type rowLUTs struct{ r, g, b [256]uint8 }

// applyToneLUT rewrites the colour channels of one row in place. Alpha is left
// alone so a transparent PNG keeps its transparency.
func applyToneLUT(row []uint8, luts *rowLUTs) {
	for i := 0; i+3 < len(row); i += 4 {
		row[i] = luts.r[row[i]]
		row[i+1] = luts.g[row[i+1]]
		row[i+2] = luts.b[row[i+2]]
	}
}

// buildRowLUTs renders the three channel curves for one row of the frame.
func buildRowLUTs(edit photoEdit, skyPull float64) rowLUTs {
	r, g, b := edit.channelParams(skyPull)
	return rowLUTs{r: buildToneLUT(r), g: buildToneLUT(g), b: buildToneLUT(b)}
}

// applyTone runs the whole tone pass over img in place. Everything but the sky
// pull is the same the whole way down the frame, so on their own one trio of
// LUTs does the lot; a sky pull varies by row instead, so each row takes LUTs
// built at that row's share of the gradient. Giving every row its own rather
// than interpolating between a handful keeps the sky free of the banding a
// coarse gradient would leave across it — and since the gradient is flat above
// the horizon and gone below the feather, only the rows inside the feather band
// actually need new ones.
func applyTone(img *image.RGBA, edit photoEdit) {
	bounds := img.Bounds()
	pull := edit.skyPullStops()
	horizon := edit.horizonPercent()

	base := buildRowLUTs(edit, 0)
	graded := base
	gradedFor := -1.0 // the weight `graded` was built at; no weight is negative
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		luts := &base
		if pull != 0 {
			weight := skyGradient(y-bounds.Min.Y, bounds.Dy(), horizon)
			if weight > 0 {
				if weight != gradedFor {
					graded = buildRowLUTs(edit, pull*weight)
					gradedFor = weight
				}
				luts = &graded
			}
		}
		start := img.PixOffset(bounds.Min.X, y)
		applyToneLUT(img.Pix[start:start+bounds.Dx()*4], luts)
	}
}

// applyOrientation bakes an EXIF orientation into the pixels. The browser
// rotates a photo before showing it, so the crop rectangle the user drew is
// relative to the rotated frame; rotating here makes the two agree.
func applyOrientation(src image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return src
	}
	in := toRGBA(src)
	w, h := in.Bounds().Dx(), in.Bounds().Dy()

	outW, outH := w, h
	if orientation >= 5 { // 5-8 transpose the axes
		outW, outH = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, outW, outH))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orientation {
			case 2: // mirrored horizontally
				dx, dy = w-1-x, y
			case 3: // rotated 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirrored vertically
				dx, dy = x, h-1-y
			case 5: // mirrored then rotated 270 CW
				dx, dy = y, x
			case 6: // rotated 90 CW
				dx, dy = h-1-y, x
			case 7: // mirrored then rotated 90 CW
				dx, dy = h-1-y, w-1-x
			case 8: // rotated 270 CW
				dx, dy = y, w-1-x
			}
			si := in.PixOffset(x, y)
			di := out.PixOffset(dx, dy)
			copy(out.Pix[di:di+4], in.Pix[si:si+4])
		}
	}
	return out
}

// cropImage returns the sub-image described by c, clamped to at least one pixel.
func cropImage(img image.Image, c cropRect) image.Image {
	b := img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())

	x0 := b.Min.X + int(math.Round(clamp01(c.X)*w))
	y0 := b.Min.Y + int(math.Round(clamp01(c.Y)*h))
	x1 := b.Min.X + int(math.Round(clamp01(c.X+c.W)*w))
	y1 := b.Min.Y + int(math.Round(clamp01(c.Y+c.H)*h))
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}

	rect := image.Rect(x0, y0, x1, y1).Intersect(b)
	if rect.Empty() {
		return img
	}
	if sub, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return sub.SubImage(rect)
	}
	return img
}

// jpegExifSegment returns the complete APP1 EXIF segment (marker, length and
// payload) of a JPEG, or nil when the file carries none.
func jpegExifSegment(data []byte) []byte {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil
	}
	exifHeader := []byte("Exif\x00\x00")
	off := 2
	for off+4 <= len(data) {
		if data[off] != 0xFF {
			return nil
		}
		marker := data[off+1]
		// Standalone markers carry no length field.
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD9) {
			off += 2
			continue
		}
		if marker == 0xDA { // start of scan — EXIF only appears before this
			return nil
		}
		segLen := int(binary.BigEndian.Uint16(data[off+2:]))
		if segLen < 2 || off+2+segLen > len(data) {
			return nil
		}
		if marker == 0xE1 && segLen >= 2+len(exifHeader)+8 && bytes.HasPrefix(data[off+4:], exifHeader) {
			return data[off : off+2+segLen]
		}
		off += 2 + segLen
	}
	return nil
}

// clearExifOrientation rewrites the orientation tag of an APP1 segment to 1.
// The render bakes the rotation into the pixels, so a viewer that applied the
// original tag as well would turn the photo a second time.
func clearExifOrientation(segment []byte) {
	const headerLen = 4 + 6 // FFE1 + length + "Exif\0\0"
	if len(segment) < headerLen+8 {
		return
	}
	tiff := segment[headerLen:]

	var bo binary.ByteOrder
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		bo = binary.LittleEndian
	case tiff[0] == 'M' && tiff[1] == 'M':
		bo = binary.BigEndian
	default:
		return
	}
	if bo.Uint16(tiff[2:]) != 42 {
		return
	}

	ifd := int(bo.Uint32(tiff[4:]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return
	}
	n := int(bo.Uint16(tiff[ifd:]))
	for i := 0; i < n; i++ {
		e := ifd + 2 + i*12
		if e+12 > len(tiff) {
			return
		}
		if bo.Uint16(tiff[e:]) == 0x0112 { // Orientation
			// A SHORT sits in the first two bytes of the value field; the
			// remaining two are padding.
			bo.PutUint16(tiff[e+8:], 1)
			bo.PutUint16(tiff[e+10:], 0)
			return
		}
	}
}

// jpegInsertExif splices an APP1 segment in directly after the SOI marker.
func jpegInsertExif(jpg, segment []byte) []byte {
	if len(segment) == 0 || len(jpg) < 2 || jpg[0] != 0xFF || jpg[1] != 0xD8 {
		return jpg
	}
	out := make([]byte, 0, len(jpg)+len(segment))
	out = append(out, jpg[:2]...)
	out = append(out, segment...)
	return append(out, jpg[2:]...)
}

// exifOrientation reads the EXIF orientation of a JPEG, defaulting to 1
// (upright) for files without one.
func exifOrientation(data []byte) int {
	tiff, err := jpegExtractExifTIFF(data)
	if err != nil {
		return 1
	}
	meta, err := parseExifTIFF(tiff)
	if err != nil || meta.Orientation < 1 || meta.Orientation > 8 {
		return 1
	}
	return meta.Orientation
}

// renderEdit applies an edit to the bytes of a pristine original and returns
// the encoded result, in the same format the source came in.
func renderEdit(src []byte, edit photoEdit) ([]byte, error) {
	img, format, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}

	img = applyOrientation(img, exifOrientation(src))

	// The tone pass runs before the crop so the sky gradient lands on the photo
	// where the editor previewed it, over the whole frame. Cropping first would
	// re-anchor the gradient to whatever the crop happened to contain.
	out := toRGBA(img)
	applyTone(out, edit)
	if edit.Crop != nil && !edit.Crop.isFull() {
		out = toRGBA(cropImage(out, *edit.Crop))
	}

	var buf bytes.Buffer
	if format == "png" {
		if err := png.Encode(&buf, out); err != nil {
			return nil, fmt.Errorf("encoding PNG: %w", err)
		}
		return buf.Bytes(), nil
	}
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: editJPEGQuality}); err != nil {
		return nil, fmt.Errorf("encoding JPEG: %w", err)
	}

	// Carry the camera settings across so the EXIF overlay, and anything the
	// photo is later exported to, still knows how the shot was taken.
	segment := jpegExifSegment(src)
	if segment == nil {
		return buf.Bytes(), nil
	}
	patched := make([]byte, len(segment))
	copy(patched, segment)
	clearExifOrientation(patched)
	return jpegInsertExif(buf.Bytes(), patched), nil
}

// readEdits loads a session's sidecar. A missing or unreadable file simply
// means nothing in the folder has been edited yet.
func readEdits(directory string) map[string]photoEdit {
	edits := map[string]photoEdit{}
	path, err := safePhotoPath(directory, editsFileName)
	if err != nil {
		return edits
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return edits
	}
	if err := json.Unmarshal(data, &edits); err != nil {
		log.Printf("Ignoring unreadable edit record %s: %v", path, err)
		return map[string]photoEdit{}
	}
	return edits
}

// writeEdits replaces a session's sidecar, removing it once nothing is edited.
func writeEdits(directory string, edits map[string]photoEdit) error {
	path, err := safePhotoPath(directory, editsFileName)
	if err != nil {
		return err
	}
	if len(edits) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(edits, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// backedUpOriginals lists the photos in a session that have a backup in
// unedited/. It is the ground truth for "this photo was edited": the sidecar
// can be deleted by hand, the backup is what the revert actually needs.
func backedUpOriginals(directory string) map[string]bool {
	names := map[string]bool{}
	dir, err := safePhotoPath(directory, uneditedDirName)
	if err != nil {
		return names
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return names
	}
	for _, entry := range entries {
		if !entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			names[entry.Name()] = true
		}
	}
	return names
}

// writeFileAtomic writes data to path via a temp file in the same directory so
// a reader can never see a half-written photo.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// dropThumbnail removes a cached thumbnail so the next request regenerates it
// from the photo as it now stands.
func dropThumbnail(directory, filename string) {
	if strings.Contains(directory, "..") || !validPhotoName(filename) {
		return
	}
	path := filepath.Join(thumbnailCacheDir, directory, filename)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("Failed to drop thumbnail %s: %v", path, err)
	}
}

// syncSelectedCopy mirrors bytes into the session's selected/ folder when the
// photo has already been saved there, so an export never ships a stale render.
func syncSelectedCopy(directory, filename string, data []byte) {
	path, err := safePhotoPath(directory, "selected", filename)
	if err != nil {
		return
	}
	if _, err := os.Stat(path); err != nil {
		return // not saved yet; saving later copies the current file
	}
	if err := writeFileAtomic(path, data); err != nil {
		log.Printf("Failed to update selected copy %s: %v", path, err)
	}
}

// removeEditArtifacts deletes the backup and edit record for a photo. It is
// called both by revert and by the delete-from-disk path, which would
// otherwise leave an orphaned original behind.
func removeEditArtifacts(directory, filename string) {
	if !validPhotoName(filename) {
		return
	}
	if path, err := safePhotoPath(directory, uneditedDirName, filename); err == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("Failed to remove backup %s: %v", path, err)
		}
	}
	// Best-effort: succeeds only once the last backup in the folder is gone.
	if dir, err := safePhotoPath(directory, uneditedDirName); err == nil {
		os.Remove(dir)
	}

	editsMu.Lock()
	defer editsMu.Unlock()
	edits := readEdits(directory)
	if _, ok := edits[filename]; ok {
		delete(edits, filename)
		if err := writeEdits(directory, edits); err != nil {
			log.Printf("Failed to update edit record for %s: %v", directory, err)
		}
	}
}

// editsHandler reports which photos in a session are edited, and with what
// settings, so the UI can badge them and reopen the editor where it left off.
func editsHandler(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	if directory == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing 'directory' query parameter")
		return
	}
	if _, err := safePhotoPath(directory); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid directory")
		return
	}

	editsMu.Lock()
	edits := readEdits(directory)
	editsMu.Unlock()

	// A photo with a backup but no record was edited by a version that lost
	// its sidecar; still mark it edited so it can be compared and reverted.
	for name := range backedUpOriginals(directory) {
		if _, ok := edits[name]; !ok {
			edits[name] = photoEdit{}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(edits)
}

// editPhotoHandler renders an edit over the pristine original, backing that
// original up on the first edit of a photo.
func editPhotoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Directory   string    `json:"directory"`
		Photo       string    `json:"photo"`
		Crop        *cropRect `json:"crop"`
		Temperature float64   `json:"temperature"`
		Tint        float64   `json:"tint"`
		Exposure    float64   `json:"exposure"`
		Black       float64   `json:"black"`
		Highlights  float64   `json:"highlights"`
		Shadows     float64   `json:"shadows"`
		Sky         float64   `json:"sky"`
		Horizon     float64   `json:"horizon"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Directory == "" || req.Photo == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing 'directory' or 'photo' in request")
		return
	}
	if !validPhotoName(req.Photo) {
		writeJSONError(w, http.StatusBadRequest, "Invalid photo name")
		return
	}
	if !isEditableImage(req.Photo) {
		writeJSONError(w, http.StatusBadRequest, "Only JPEG and PNG files can be edited")
		return
	}
	if req.Crop != nil && !req.Crop.valid() {
		writeJSONError(w, http.StatusBadRequest, "Invalid crop rectangle")
		return
	}
	if math.Abs(req.Exposure) > 5 || math.Abs(req.Black) > 100 || math.Abs(req.Highlights) > 100 ||
		math.Abs(req.Temperature) > 100 || math.Abs(req.Tint) > 100 {
		writeJSONError(w, http.StatusBadRequest, "Adjustment out of range")
		return
	}
	if req.Shadows < 0 || req.Shadows > 100 ||
		req.Sky < 0 || req.Sky > 100 || req.Horizon < 0 || req.Horizon > 100 {
		writeJSONError(w, http.StatusBadRequest, "Shadow or sky adjustment out of range")
		return
	}

	edit := photoEdit{
		Crop:        req.Crop,
		Temperature: req.Temperature,
		Tint:        req.Tint,
		Exposure:    req.Exposure,
		Black:       req.Black,
		Highlights:  req.Highlights,
		Shadows:     req.Shadows,
		Sky:         req.Sky,
		Horizon:     req.Horizon,
	}
	if edit.isIdentity() {
		// Nothing to apply — restore the original rather than re-encoding it.
		restoreOriginal(w, req.Directory, req.Photo)
		return
	}

	photoPath, err := safePhotoPath(req.Directory, req.Photo)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid photo path")
		return
	}
	originalPath, err := safePhotoPath(req.Directory, uneditedDirName, req.Photo)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid photo path")
		return
	}

	if _, err := os.Stat(originalPath); os.IsNotExist(err) {
		if _, err := os.Stat(photoPath); err != nil {
			writeJSONError(w, http.StatusNotFound, "Photo not found")
			return
		}
		if err := os.MkdirAll(filepath.Dir(originalPath), 0755); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Failed to create backup directory")
			return
		}
		if err := copyFile(photoPath, originalPath); err != nil {
			log.Printf("Failed to back up original %s: %v", photoPath, err)
			writeJSONError(w, http.StatusInternalServerError, "Failed to back up the original photo")
			return
		}
	}

	original, err := os.ReadFile(originalPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to read the original photo")
		return
	}

	rendered, err := renderEdit(original, edit)
	if err != nil {
		log.Printf("Failed to render edit for %s/%s: %v", req.Directory, req.Photo, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to apply the edit")
		return
	}
	if err := writeFileAtomic(photoPath, rendered); err != nil {
		log.Printf("Failed to write edited photo %s: %v", photoPath, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to save the edited photo")
		return
	}
	syncSelectedCopy(req.Directory, req.Photo, rendered)
	dropThumbnail(req.Directory, req.Photo)

	edit.EditedAt = time.Now().Format(time.RFC3339)
	editsMu.Lock()
	edits := readEdits(req.Directory)
	edits[req.Photo] = edit
	err = writeEdits(req.Directory, edits)
	editsMu.Unlock()
	if err != nil {
		log.Printf("Failed to record edit for %s/%s: %v", req.Directory, req.Photo, err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "edited",
		"photo":  req.Photo,
		"edit":   edit,
	})
}

// revertPhotoHandler puts the pristine original back in place and forgets the
// adjustments that produced the edited version.
func revertPhotoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var req struct {
		Directory string `json:"directory"`
		Photo     string `json:"photo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Directory == "" || req.Photo == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing 'directory' or 'photo' in request")
		return
	}
	if !validPhotoName(req.Photo) {
		writeJSONError(w, http.StatusBadRequest, "Invalid photo name")
		return
	}
	restoreOriginal(w, req.Directory, req.Photo)
}

// restoreOriginal copies the backup back over the edited photo and clears the
// edit's traces, replying with the JSON both revert and a no-op edit return.
func restoreOriginal(w http.ResponseWriter, directory, photo string) {
	photoPath, err := safePhotoPath(directory, photo)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid photo path")
		return
	}
	originalPath, err := safePhotoPath(directory, uneditedDirName, photo)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid photo path")
		return
	}

	original, err := os.ReadFile(originalPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Never edited: the photo on disk already is the original.
			removeEditArtifacts(directory, photo)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "reverted", "photo": photo})
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "Failed to read the original photo")
		return
	}

	if err := writeFileAtomic(photoPath, original); err != nil {
		log.Printf("Failed to restore original %s: %v", photoPath, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to restore the original photo")
		return
	}
	syncSelectedCopy(directory, photo, original)
	dropThumbnail(directory, photo)
	removeEditArtifacts(directory, photo)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "reverted", "photo": photo})
}
