package main

import (
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
