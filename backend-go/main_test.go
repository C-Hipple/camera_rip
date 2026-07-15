package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
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
