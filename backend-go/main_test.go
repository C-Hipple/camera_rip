package main

import (
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
