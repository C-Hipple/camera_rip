package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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
	identity := buildToneLUT(0, 0)
	for i := range identity {
		if int(identity[i]) != i {
			t.Fatalf("neutral LUT changed %d to %d", i, identity[i])
		}
	}

	brighter := buildToneLUT(1, 0)
	darker := buildToneLUT(-1, 0)
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

	crushed := buildToneLUT(0, 100)
	lifted := buildToneLUT(0, -100)
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
		exposure float64
		black    float64
		input    int
		want     uint8
	}{
		{1, 0, 128, 175},
		{-1, 0, 128, 93},
		{0, 50, 128, 110},
		{0, -50, 0, 28},
		{0.5, 25, 200, 233},
		{1, 0, 64, 88},
	}
	for _, tt := range tests {
		got := buildToneLUT(tt.exposure, tt.black)[tt.input]
		if got != tt.want {
			t.Errorf("buildToneLUT(%v, %v)[%d] = %d, want %d", tt.exposure, tt.black, tt.input, got, tt.want)
		}
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
		`{"directory":"`+directory+`","photo":"100_IMG_0001.png","crop":{"x":0,"y":0,"w":0.5,"h":1},"exposure":0.5,"black":10}`)
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
