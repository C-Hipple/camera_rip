package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectCameraBrand(t *testing.T) {
	tests := []struct {
		folderName string
		wantSuffix string
	}{
		{"100CANON", "CANON"},
		{"101CANON", "CANON"},
		{"100OLYMP", "OLYMP"},
		{"100OMSYS", "OMSYS"},
		{"999OMSYS", "OMSYS"},
		{"100NIKON", ""}, // Not supported yet
		{"DCIM", ""},
	}

	for _, tt := range tests {
		got := detectCameraBrand(tt.folderName)
		if tt.wantSuffix == "" {
			if got != nil {
				t.Errorf("detectCameraBrand(%q) = %v, want nil", tt.folderName, got.suffix)
			}
		} else {
			if got == nil {
				t.Errorf("detectCameraBrand(%q) = nil, want %q", tt.folderName, tt.wantSuffix)
			} else if got.suffix != tt.wantSuffix {
				t.Errorf("detectCameraBrand(%q) = %q, want %q", tt.folderName, got.suffix, tt.wantSuffix)
			}
		}
	}
}

func TestGetDCIMPrefix(t *testing.T) {
	tests := []struct {
		dir  string
		want string
	}{
		{"100CANON", "100"},
		{"101OLYMP", "101"},
		{"102OMSYS", "102"},
		{"ABC", ""},
		{"12", ""},
		{"123", "123"},
	}

	for _, tt := range tests {
		got := getDCIMPrefix(tt.dir)
		if got != tt.want {
			t.Errorf("getDCIMPrefix(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

func TestSafePhotoPath(t *testing.T) {
	photoBaseDir = filepath.Join("/tmp", "camera-rip-test", "photos")

	// Paths that must resolve inside the base directory.
	okCases := []struct {
		name string
		elem []string
	}{
		{"simple directory", []string{"2026-01-01_batch"}},
		{"nested selected", []string{"2026-01-01_batch", "selected"}},
		{"dir and file", []string{"2026-01-01_batch", "IMG_0001.JPG"}},
		{"base itself", []string{""}},
	}
	for _, tt := range okCases {
		got, err := safePhotoPath(tt.elem...)
		if err != nil {
			t.Errorf("safePhotoPath(%v) unexpected error: %v", tt.elem, err)
			continue
		}
		base, _ := filepath.Abs(photoBaseDir)
		if got != base && !filepathHasPrefix(got, base) {
			t.Errorf("safePhotoPath(%v) = %q, want within %q", tt.elem, got, base)
		}
	}

	// Paths that attempt to escape the base directory must error.
	badCases := []struct {
		name string
		elem []string
	}{
		{"parent traversal in dir", []string{"../../Documents"}},
		{"traversal with file", []string{"..", "..", "secret.txt"}},
		{"embedded traversal", []string{"batch/../../etc"}},
		{"traversal in filename segment", []string{"batch", "../../../etc/passwd"}},
	}
	for _, tt := range badCases {
		if _, err := safePhotoPath(tt.elem...); err == nil {
			t.Errorf("safePhotoPath(%v) = nil error, want traversal rejection", tt.elem)
		}
	}
}

func filepathHasPrefix(path, prefix string) bool {
	return len(path) >= len(prefix) && path[:len(prefix)] == prefix
}

func TestIsRawFile(t *testing.T) {
	tests := []struct {
		filename string
		want     bool
	}{
		{"P4061482.ORF", true},
		{"IMG_0001.CR3", true},
		{"IMG_0001.JPG", false},
		{"test.txt", false},
		{"ORF.JPG", false},
	}

	for _, tt := range tests {
		got := isRawFile(tt.filename)
		if got != tt.want {
			t.Errorf("isRawFile(%q) = %v, want %v", tt.filename, got, tt.want)
		}
	}
}

// buildTestExifTIFF constructs a minimal little-endian EXIF TIFF block:
// IFD0 holds only the Exif SubIFD pointer; the SubIFD holds exposure 1/250s,
// f/5.6, ISO 400, and 50mm focal length.
func buildTestExifTIFF() []byte {
	buf := make([]byte, 104)
	le := binary.LittleEndian
	copy(buf, "II")
	le.PutUint16(buf[2:], 42)
	le.PutUint32(buf[4:], 8)

	// IFD0: one entry pointing at the Exif SubIFD at offset 26.
	le.PutUint16(buf[8:], 1)
	le.PutUint16(buf[10:], 0x8769)
	le.PutUint16(buf[12:], 4) // LONG
	le.PutUint32(buf[14:], 1)
	le.PutUint32(buf[18:], 26)
	le.PutUint32(buf[22:], 0) // no next IFD

	le.PutUint16(buf[26:], 4)
	entry := func(i int, tag, typ uint16, val uint32) {
		e := 28 + i*12
		le.PutUint16(buf[e:], tag)
		le.PutUint16(buf[e+2:], typ)
		le.PutUint32(buf[e+4:], 1)
		le.PutUint32(buf[e+8:], val)
	}
	entry(0, 0x829A, 5, 80)   // ExposureTime -> rational at 80
	entry(1, 0x829D, 5, 88)   // FNumber -> rational at 88
	entry(2, 0x8827, 3, 400)  // ISO 400, inline SHORT
	entry(3, 0x920A, 5, 96)   // FocalLength -> rational at 96
	le.PutUint32(buf[76:], 0) // no next IFD

	le.PutUint32(buf[80:], 1)
	le.PutUint32(buf[84:], 250) // 1/250s
	le.PutUint32(buf[88:], 56)
	le.PutUint32(buf[92:], 10) // f/5.6
	le.PutUint32(buf[96:], 50)
	le.PutUint32(buf[100:], 1) // 50mm
	return buf
}

func TestParseExifTIFF(t *testing.T) {
	meta, err := parseExifTIFF(buildTestExifTIFF())
	if err != nil {
		t.Fatalf("parseExifTIFF() error = %v", err)
	}
	want := photoMetadata{ShutterSpeed: "1/250s", Aperture: "f/5.6", ISO: "ISO 400", FocalLength: "50mm"}
	if meta != want {
		t.Errorf("parseExifTIFF() = %+v, want %+v", meta, want)
	}
}

func TestExtractPhotoMetadataFromJPEG(t *testing.T) {
	tiff := buildTestExifTIFF()
	payloadLen := 2 + 6 + len(tiff)
	var jpg []byte
	jpg = append(jpg, 0xFF, 0xD8) // SOI
	jpg = append(jpg, 0xFF, 0xE1, byte(payloadLen>>8), byte(payloadLen))
	jpg = append(jpg, []byte("Exif\x00\x00")...)
	jpg = append(jpg, tiff...)
	jpg = append(jpg, 0xFF, 0xD9) // EOI

	path := filepath.Join(t.TempDir(), "test.jpg")
	if err := os.WriteFile(path, jpg, 0644); err != nil {
		t.Fatal(err)
	}
	meta, err := extractPhotoMetadata(path)
	if err != nil {
		t.Fatalf("extractPhotoMetadata() error = %v", err)
	}
	want := photoMetadata{ShutterSpeed: "1/250s", Aperture: "f/5.6", ISO: "ISO 400", FocalLength: "50mm"}
	if meta != want {
		t.Errorf("extractPhotoMetadata() = %+v, want %+v", meta, want)
	}
}

func TestSDCleanupTarget(t *testing.T) {
	tests := []struct {
		name     string
		wantKind string
		wantOK   bool
	}{
		{".Trashes", "trash", true},
		{".Trash-1000", "trash", true},
		{".Trash-501", "trash", true},
		{".fseventsd", "system", true},
		{".Spotlight-V100", "system", true},
		{".TemporaryItems", "system", true},
		{"DCIM", "", false},
		{".thumbnails", "", false},
		{"Trashes", "", false},
	}
	for _, tt := range tests {
		kind, ok := sdCleanupTarget(tt.name)
		if kind != tt.wantKind || ok != tt.wantOK {
			t.Errorf("sdCleanupTarget(%q) = (%q, %v), want (%q, %v)", tt.name, kind, ok, tt.wantKind, tt.wantOK)
		}
	}
}

func TestScanSDCleanupTargets(t *testing.T) {
	mount := t.TempDir()

	// Camera content that must never be reported.
	if err := os.MkdirAll(filepath.Join(mount, "DCIM", "100CANON"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mount, "DCIM", "100CANON", "IMG_0001.JPG"), []byte("photo"), 0644); err != nil {
		t.Fatal(err)
	}

	// macOS trash with a nested per-user folder, and a Linux trash folder.
	if err := os.MkdirAll(filepath.Join(mount, ".Trashes", "501"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mount, ".Trashes", "501", "IMG_0002.JPG"), []byte("trashed-file"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(mount, ".Trash-1000", "files"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mount, ".Trash-1000", "files", "IMG_0003.ORF"), []byte("trashed-raw!"), 0644); err != nil {
		t.Fatal(err)
	}

	items := scanSDCleanupTargets(mount)
	if len(items) != 2 {
		t.Fatalf("scanSDCleanupTargets() found %d items, want 2: %+v", len(items), items)
	}
	byName := map[string]sdCleanupItem{}
	for _, item := range items {
		byName[item.Name] = item
	}
	if it, ok := byName[".Trashes"]; !ok || it.Kind != "trash" || it.Files != 1 || it.Size != int64(len("trashed-file")) {
		t.Errorf(".Trashes item = %+v, want trash kind with 1 file of %d bytes", byName[".Trashes"], len("trashed-file"))
	}
	if it, ok := byName[".Trash-1000"]; !ok || it.Kind != "trash" || it.Files != 1 || it.Size != int64(len("trashed-raw!")) {
		t.Errorf(".Trash-1000 item = %+v, want trash kind with 1 file of %d bytes", byName[".Trash-1000"], len("trashed-raw!"))
	}
}

func TestFormatShutterSpeed(t *testing.T) {
	tests := []struct {
		num, den uint32
		want     string
	}{
		{1, 250, "1/250s"},
		{1, 8000, "1/8000s"},
		{25, 10, "2.5s"},
		{30, 1, "30s"},
		{1, 1, "1s"},
		{0, 250, ""},
	}
	for _, tt := range tests {
		if got := formatShutterSpeed(tt.num, tt.den); got != tt.want {
			t.Errorf("formatShutterSpeed(%d, %d) = %q, want %q", tt.num, tt.den, got, tt.want)
		}
	}
}

func TestNormalizeTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"film, golden hour", "film, golden hour"},
		{"#film #mono", "film, mono"},
		{"  film ,  , mono ", "film, mono"},
		{"#film, golden hour, #mono", "film, golden hour, mono"},
		{"film, Film, FILM", "film"},
		{"#golden hour", "golden hour"},
		{"", ""},
		{"   ", ""},
		{"#", ""},
	}
	for _, tt := range tests {
		if got := normalizeTags(tt.input); got != tt.want {
			t.Errorf("normalizeTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGalleryPhotoURL(t *testing.T) {
	galleryBaseURL = "https://gallery.example"
	tests := []struct {
		name   string
		parsed map[string]interface{}
		want   string
	}{
		{"absolute url", map[string]interface{}{"url": "https://gallery.example/p/42"}, "https://gallery.example/p/42"},
		{"relative url", map[string]interface{}{"url": "/p/42"}, "https://gallery.example/p/42"},
		{"page preferred over image", map[string]interface{}{"url": "/i/42.jpg", "page_url": "/p/42"}, "https://gallery.example/p/42"},
		{"nested urls object", map[string]interface{}{"urls": map[string]interface{}{"page": "/p/42"}}, "https://gallery.example/p/42"},
		{"no url at all", map[string]interface{}{"id": "42"}, ""},
		{"empty response", map[string]interface{}{}, ""},
	}
	for _, tt := range tests {
		if got := galleryPhotoURL(tt.parsed); got != tt.want {
			t.Errorf("%s: galleryPhotoURL() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestGalleryErrorMessage(t *testing.T) {
	if got := galleryErrorMessage(400, map[string]interface{}{"error": "no file"}, []byte(`{"error":"no file"}`)); got != "no file" {
		t.Errorf("galleryErrorMessage() = %q, want the gallery's own error", got)
	}
	if got := galleryErrorMessage(401, map[string]interface{}{}, nil); !strings.Contains(got, "password") {
		t.Errorf("galleryErrorMessage(401) = %q, want a message about the password", got)
	}
	if got := galleryErrorMessage(500, map[string]interface{}{}, []byte("boom")); !strings.Contains(got, "boom") {
		t.Errorf("galleryErrorMessage(500) = %q, want the response body included", got)
	}
}

// galleryUploadRequest is what the fake gallery saw on its /api/upload route.
type galleryUploadRequest struct {
	password string
	title    string
	tags     string
	filename string
	body     []byte
}

// newFakeGallery stands in for the gallery API, recording the multipart upload
// it receives and answering with the given status and JSON body.
func newFakeGallery(t *testing.T, status int, response string, got *galleryUploadRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/upload" {
			t.Errorf("gallery got request for %q, want /api/upload", r.URL.Path)
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Errorf("gallery could not parse multipart body: %v", err)
			return
		}
		got.password = r.FormValue("password")
		got.title = r.FormValue("title")
		got.tags = r.FormValue("tags")
		if file, header, err := r.FormFile("photo"); err == nil {
			defer file.Close()
			got.filename = header.Filename
			got.body, _ = io.ReadAll(file)
		} else {
			t.Errorf("gallery got no 'photo' file: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(response))
	}))
}

// writeTestFile creates dir if needed and drops a one-byte file in it.
func writeTestFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

func getDirectories(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/directories", nil)
	w := httptest.NewRecorder()
	listDirectoriesHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list directories returned %d, want 200: %s", w.Code, w.Body.String())
	}
	return w
}

func TestListDirectoriesHandler(t *testing.T) {
	photoBaseDir = t.TempDir()

	// A reviewed session: three viewable photos, one of them exported. The raw
	// beside it lives in selected/raw and must not inflate the selected count.
	party := filepath.Join(photoBaseDir, "2025-12-11 Holiday Party")
	writeTestFile(t, party, "100_IMG_0001.JPG")
	writeTestFile(t, party, "100_IMG_0002.JPG")
	writeTestFile(t, party, "100_IMG_0003.jpeg")
	writeTestFile(t, filepath.Join(party, "selected"), "100_IMG_0002.JPG")
	writeTestFile(t, filepath.Join(party, "selected", "raw"), "100_IMG_0002.CR3")

	// An untouched session has no selected/ folder at all. Its macOS sidecar
	// and stray text file are not photos.
	hike := filepath.Join(photoBaseDir, "2025-11-02 Hike")
	writeTestFile(t, hike, "100_IMG_0009.JPG")
	writeTestFile(t, hike, "._100_IMG_0009.JPG")
	writeTestFile(t, hike, "notes.txt")
	// An edited photo stays one photo: its pristine backup lives in unedited/
	// and its settings sidecar is not an image.
	writeTestFile(t, filepath.Join(hike, uneditedDirName), "100_IMG_0009.JPG")
	writeTestFile(t, hike, editsFileName)

	// A raw-only import counts its RAWs, the same files the review UI shows.
	rawOnly := filepath.Join(photoBaseDir, "2025-10-05 Raw Only")
	writeTestFile(t, rawOnly, "100_IMG_0100.CR3")

	// The thumbnail cache is not a session.
	writeTestFile(t, filepath.Join(photoBaseDir, ".thumbnails", "2025-11-02 Hike"), "100_IMG_0009.JPG")

	var got []sessionDirectory
	if err := json.Unmarshal(getDirectories(t).Body.Bytes(), &got); err != nil {
		t.Fatalf("could not decode reply: %v", err)
	}

	// Newest first, as the selector lists them.
	want := []sessionDirectory{
		{Name: "2025-12-11 Holiday Party", PhotoCount: 3, SelectedCount: 1},
		{Name: "2025-11-02 Hike", PhotoCount: 1, SelectedCount: 0},
		{Name: "2025-10-05 Raw Only", PhotoCount: 1, SelectedCount: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sessions %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("session %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The frontend maps over the reply, so an empty photo library must still be a
// JSON array rather than null.
func TestListDirectoriesHandlerEmptyLibrary(t *testing.T) {
	photoBaseDir = t.TempDir()

	if body := strings.TrimSpace(getDirectories(t).Body.String()); body != "[]" {
		t.Errorf("empty photo library returned %q, want %q", body, "[]")
	}
}

// withTestPhoto points photoBaseDir at a temp directory holding one photo.
func withTestPhoto(t *testing.T, name string, contents []byte) (directory string) {
	t.Helper()
	photoBaseDir = t.TempDir()
	directory = "2026-01-01_batch"
	dir := filepath.Join(photoBaseDir, directory)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), contents, 0644); err != nil {
		t.Fatal(err)
	}
	return directory
}

func postGalleryUpload(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/gallery-upload", strings.NewReader(body))
	w := httptest.NewRecorder()
	galleryUploadHandler(w, req)
	return w
}

func TestGalleryUploadHandler(t *testing.T) {
	var got galleryUploadRequest
	server := newFakeGallery(t, http.StatusCreated, `{"id":"42","page_url":"/p/42"}`, &got)
	defer server.Close()

	directory := withTestPhoto(t, "100_IMG_0001.JPG", []byte("jpeg-bytes"))
	galleryBaseURL = server.URL
	galleryPassword = "hunter2"

	w := postGalleryUpload(t, `{"directory":"`+directory+`","filename":"100_IMG_0001.JPG","title":"Sunrise","tags":"#film #mono"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("gallery upload returned %d, want 200: %s", w.Code, w.Body.String())
	}

	if got.password != "hunter2" {
		t.Errorf("gallery received password %q, want %q", got.password, "hunter2")
	}
	if got.title != "Sunrise" {
		t.Errorf("gallery received title %q, want %q", got.title, "Sunrise")
	}
	if got.tags != "film, mono" {
		t.Errorf("gallery received tags %q, want %q", got.tags, "film, mono")
	}
	if got.filename != "100_IMG_0001.JPG" {
		t.Errorf("gallery received filename %q, want %q", got.filename, "100_IMG_0001.JPG")
	}
	if string(got.body) != "jpeg-bytes" {
		t.Errorf("gallery received body %q, want the photo's bytes", got.body)
	}

	var reply struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("could not decode reply: %v", err)
	}
	if reply.Status != "uploaded" {
		t.Errorf("reply status = %q, want %q", reply.Status, "uploaded")
	}
	if reply.URL != server.URL+"/p/42" {
		t.Errorf("reply url = %q, want %q", reply.URL, server.URL+"/p/42")
	}
}

func TestGalleryUploadHandlerRelaysGalleryErrors(t *testing.T) {
	var got galleryUploadRequest
	server := newFakeGallery(t, http.StatusUnauthorized, `{"error":"wrong password"}`, &got)
	defer server.Close()

	directory := withTestPhoto(t, "100_IMG_0001.JPG", []byte("jpeg-bytes"))
	galleryBaseURL = server.URL
	galleryPassword = "wrong"

	w := postGalleryUpload(t, `{"directory":"`+directory+`","filename":"100_IMG_0001.JPG"}`)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("gallery upload returned %d, want the gallery's 401", w.Code)
	}
	var reply struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("could not decode reply: %v", err)
	}
	if reply.Error != "wrong password" {
		t.Errorf("reply error = %q, want the gallery's own message", reply.Error)
	}
	if got.title != "" || got.tags != "" {
		t.Errorf("optional fields were sent empty rather than omitted: title=%q tags=%q", got.title, got.tags)
	}
}

func TestGalleryUploadHandlerRejectsBadRequests(t *testing.T) {
	directory := withTestPhoto(t, "100_IMG_0001.JPG", []byte("jpeg-bytes"))
	galleryBaseURL = "https://gallery.example"
	galleryPassword = "hunter2"

	tests := []struct {
		name string
		body string
		want int
	}{
		{"missing filename", `{"directory":"` + directory + `"}`, http.StatusBadRequest},
		{"missing directory", `{"filename":"100_IMG_0001.JPG"}`, http.StatusBadRequest},
		{"path traversal", `{"directory":"` + directory + `","filename":"../../secret.jpg"}`, http.StatusBadRequest},
		{"unreadable photo", `{"directory":"` + directory + `","filename":"nope.JPG"}`, http.StatusBadRequest},
		{"malformed json", `{`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		if w := postGalleryUpload(t, tt.body); w.Code != tt.want {
			t.Errorf("%s: returned %d, want %d (%s)", tt.name, w.Code, tt.want, w.Body.String())
		}
	}

	// With no gallery configured the endpoint says so rather than trying.
	galleryBaseURL, galleryPassword = "", ""
	if w := postGalleryUpload(t, `{"directory":"`+directory+`","filename":"100_IMG_0001.JPG"}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured gallery returned %d, want 503", w.Code)
	}
}
