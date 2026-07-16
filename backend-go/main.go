package main

import (
	"bytes"
	"embed"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"io/fs"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nfnt/resize"
	"regexp"
)

//go:embed all:frontend/build
var frontend embed.FS

var (
	photoBaseDir      string
	thumbnailCacheDir string
	thumbnailSize     = 200
)

type cameraBrand struct {
	suffix string // DCIM folder suffix, e.g. "CANON", "OLYMP"
	rawExt string // RAW file extension including dot, e.g. ".CR3", ".ORF"
}

var supportedBrands = []cameraBrand{
	{suffix: "CANON", rawExt: ".CR3"},
	{suffix: "OLYMP", rawExt: ".ORF"},
	{suffix: "OMSYS", rawExt: ".ORF"},
}

func detectCameraBrand(folderName string) *cameraBrand {
	upper := strings.ToUpper(folderName)
	for i := range supportedBrands {
		if strings.HasSuffix(upper, supportedBrands[i].suffix) {
			return &supportedBrands[i]
		}
	}
	return nil
}

func isRawFile(name string) bool {
	lower := strings.ToLower(name)
	for _, b := range supportedBrands {
		if strings.HasSuffix(lower, strings.ToLower(b.rawExt)) {
			return true
		}
	}
	return false
}

// extractEmbeddedJPEG returns the embedded JPEG preview bytes from a RAW file.
//
// Strategy:
//   - TIFF-based RAWs (ORF, etc.): parse TIFF IFDs and use tags 0x0201/0x0202
//     (JPEGInterchangeFormat / Length) for exact offset and length. This avoids
//     including raw sensor data that follows the JPEG in the file.
//   - ISOBMFF-based RAWs (CR3, etc.): scan for all JPEG SOI markers and return
//     the largest segment bounded by the next SOI (or EOF).
func extractEmbeddedJPEG(rawPath string) ([]byte, error) {
	data, err := os.ReadFile(rawPath)
	if err != nil {
		return nil, err
	}
	if len(data) < 8 {
		return nil, fmt.Errorf("file too small: %s", rawPath)
	}

	// TIFF magic: "II" (little-endian) or "MM" (big-endian)
	if (data[0] == 'I' && data[1] == 'I') || (data[0] == 'M' && data[1] == 'M') {
		if j, err := tiffExtractJPEG(data); err == nil {
			return j, nil
		}
	}

	return scanExtractJPEG(data, rawPath)
}

// tiffExtractJPEG walks TIFF IFD chains looking for tags 0x0201/0x0202 that
// point to an embedded JPEG preview (standard in EXIF / Olympus ORF IFD1).
func tiffExtractJPEG(data []byte) ([]byte, error) {
	var bo binary.ByteOrder
	if data[0] == 'I' {
		bo = binary.LittleEndian
	} else {
		bo = binary.BigEndian
	}
	if bo.Uint16(data[2:]) != 42 {
		return nil, fmt.Errorf("not a TIFF file")
	}

	ifdOff := bo.Uint32(data[4:])
	for ifdOff != 0 && int(ifdOff)+2 <= len(data) {
		n := int(bo.Uint16(data[ifdOff:]))
		base := int(ifdOff) + 2
		var jpegOff, jpegLen uint32
		for i := 0; i < n; i++ {
			e := base + i*12
			if e+12 > len(data) {
				break
			}
			tag := bo.Uint16(data[e:])
			val := bo.Uint32(data[e+8:])
			switch tag {
			case 0x0201:
				jpegOff = val
			case 0x0202:
				jpegLen = val
			}
		}
		if jpegOff > 0 && jpegLen > 0 && int(jpegOff)+int(jpegLen) <= len(data) {
			return data[jpegOff : jpegOff+jpegLen], nil
		}
		// Follow linked-list to next IFD
		nextOff := base + n*12
		if nextOff+4 > len(data) {
			break
		}
		ifdOff = bo.Uint32(data[nextOff:])
	}
	return nil, fmt.Errorf("JPEG offset/length tags not found in TIFF IFDs")
}

// scanExtractJPEG finds the largest JPEG segment in arbitrary binary data by
// locating all SOI markers and bounding each segment by the next SOI (or EOF).
// Used for ISOBMFF-based RAWs (CR3) where TIFF parsing doesn't apply.
func scanExtractJPEG(data []byte, rawPath string) ([]byte, error) {
	soi := []byte{0xFF, 0xD8, 0xFF}
	eoi := []byte{0xFF, 0xD9}

	var starts []int
	for off := 0; off+3 <= len(data); {
		idx := bytes.Index(data[off:], soi)
		if idx < 0 {
			break
		}
		starts = append(starts, off+idx)
		off = off + idx + 1
	}

	var best []byte
	for i, start := range starts {
		bound := len(data)
		if i+1 < len(starts) {
			bound = starts[i+1]
		}
		eoiIdx := bytes.LastIndex(data[start:bound], eoi)
		if eoiIdx < 3 {
			continue
		}
		seg := data[start : start+eoiIdx+2]
		if len(seg) > len(best) {
			best = seg
		}
	}
	if len(best) == 0 {
		return nil, fmt.Errorf("no embedded JPEG found in %s", rawPath)
	}
	return best, nil
}

// photoMetadata holds camera settings extracted from a photo's EXIF data,
// pre-formatted for display (e.g. "1/250s", "f/5.6", "ISO 400", "50mm").
type photoMetadata struct {
	ShutterSpeed string `json:"shutter_speed,omitempty"`
	Aperture     string `json:"aperture,omitempty"`
	ISO          string `json:"iso,omitempty"`
	FocalLength  string `json:"focal_length,omitempty"`
}

// trimFloat formats v with at most one decimal place, dropping a trailing ".0"
// (5.6 -> "5.6", 8.0 -> "8").
func trimFloat(v float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
}

func formatShutterSpeed(num, den uint32) string {
	if num == 0 || den == 0 {
		return ""
	}
	if num >= den {
		return trimFloat(float64(num)/float64(den)) + "s"
	}
	return fmt.Sprintf("1/%.0fs", float64(den)/float64(num))
}

// parseExifTIFF extracts camera settings from a TIFF block (the payload of a
// JPEG APP1 EXIF segment, a TIFF-based RAW, or a CR3 CMT2 box). It walks the
// IFD0 chain and the Exif SubIFD (tag 0x8769) when present; CR3 CMT2 blocks
// carry the Exif tags directly in IFD0.
func parseExifTIFF(data []byte) (photoMetadata, error) {
	var meta photoMetadata
	if len(data) < 8 {
		return meta, fmt.Errorf("EXIF data too small")
	}
	var bo binary.ByteOrder
	switch {
	case data[0] == 'I' && data[1] == 'I':
		bo = binary.LittleEndian
	case data[0] == 'M' && data[1] == 'M':
		bo = binary.BigEndian
	default:
		return meta, fmt.Errorf("invalid TIFF byte order")
	}
	// 42 is standard TIFF; 0x4F52/0x5352 are Olympus ORF variants.
	switch bo.Uint16(data[2:]) {
	case 42, 0x4F52, 0x5352:
	default:
		return meta, fmt.Errorf("invalid TIFF magic")
	}

	// readRational reads a RATIONAL value referenced by the entry at e.
	readRational := func(e int) (uint32, uint32, bool) {
		off := int(bo.Uint32(data[e+8:]))
		if off < 0 || off+8 > len(data) {
			return 0, 0, false
		}
		return bo.Uint32(data[off:]), bo.Uint32(data[off+4:]), true
	}

	var exifIFDOff uint32
	parseIFD := func(ifdOff uint32) {
		if int(ifdOff)+2 > len(data) {
			return
		}
		n := int(bo.Uint16(data[ifdOff:]))
		base := int(ifdOff) + 2
		for i := 0; i < n; i++ {
			e := base + i*12
			if e+12 > len(data) {
				break
			}
			switch bo.Uint16(data[e:]) {
			case 0x8769: // Exif SubIFD pointer
				exifIFDOff = bo.Uint32(data[e+8:])
			case 0x829A: // ExposureTime
				if num, den, ok := readRational(e); ok {
					meta.ShutterSpeed = formatShutterSpeed(num, den)
				}
			case 0x829D: // FNumber
				if num, den, ok := readRational(e); ok && den > 0 {
					meta.Aperture = "f/" + trimFloat(float64(num)/float64(den))
				}
			case 0x8827: // ISO speed (SHORT, stored inline)
				meta.ISO = fmt.Sprintf("ISO %d", bo.Uint16(data[e+8:]))
			case 0x920A: // FocalLength
				if num, den, ok := readRational(e); ok && den > 0 && num > 0 {
					meta.FocalLength = trimFloat(float64(num)/float64(den)) + "mm"
				}
			}
		}
	}

	parseIFD(bo.Uint32(data[4:]))
	if exifIFDOff != 0 {
		parseIFD(exifIFDOff)
	}
	return meta, nil
}

// jpegExtractExifTIFF returns the TIFF payload of a JPEG's APP1 EXIF segment.
func jpegExtractExifTIFF(data []byte) ([]byte, error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, fmt.Errorf("not a JPEG")
	}
	exifHeader := []byte("Exif\x00\x00")
	off := 2
	for off+4 <= len(data) {
		if data[off] != 0xFF {
			break
		}
		marker := data[off+1]
		// Standalone markers without a length field.
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD9) {
			off += 2
			continue
		}
		if marker == 0xDA { // start of scan — EXIF only appears before this
			break
		}
		segLen := int(binary.BigEndian.Uint16(data[off+2:]))
		if segLen < 2 || off+2+segLen > len(data) {
			break
		}
		if marker == 0xE1 && segLen >= 2+len(exifHeader)+8 && bytes.HasPrefix(data[off+4:], exifHeader) {
			return data[off+4+len(exifHeader) : off+2+segLen], nil
		}
		off += 2 + segLen
	}
	return nil, fmt.Errorf("no EXIF APP1 segment found")
}

// extractPhotoMetadata reads camera settings from a photo's EXIF data.
// JPEGs carry EXIF in an APP1 segment; TIFF-based RAWs (ORF) are parsed
// directly; ISOBMFF RAWs (CR3) store the Exif IFD as a bare TIFF block
// inside the CMT2 box.
func extractPhotoMetadata(path string) (photoMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return photoMetadata{}, err
	}
	if len(data) < 8 {
		return photoMetadata{}, fmt.Errorf("file too small: %s", path)
	}

	if (data[0] == 'I' && data[1] == 'I') || (data[0] == 'M' && data[1] == 'M') {
		return parseExifTIFF(data)
	}
	if data[0] == 0xFF && data[1] == 0xD8 {
		tiff, err := jpegExtractExifTIFF(data)
		if err != nil {
			return photoMetadata{}, err
		}
		return parseExifTIFF(tiff)
	}
	if idx := bytes.Index(data, []byte("CMT2")); idx >= 0 && idx+4 < len(data) {
		return parseExifTIFF(data[idx+4:])
	}
	return photoMetadata{}, fmt.Errorf("no EXIF data found in %s", path)
}

// rawAlreadyExported returns true if a raw file with the given base name (any supported
// extension) already exists in dir.
func rawAlreadyExported(dir, baseName string) bool {
	for _, b := range supportedBrands {
		if _, err := os.Stat(filepath.Join(dir, baseName+b.rawExt)); err == nil {
			return true
		}
	}
	return false
}

// safePhotoPath joins user-supplied path elements (e.g. a directory or filename
// from a request) under photoBaseDir and verifies the cleaned result stays
// within photoBaseDir. It guards every handler that builds a filesystem path
// from request input against traversal via "..". Returns the cleaned absolute
// path, or an error if the result would escape photoBaseDir.
func safePhotoPath(elem ...string) (string, error) {
	base, err := filepath.Abs(photoBaseDir)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Join(append([]string{base}, elem...)...))
	if err != nil {
		return "", err
	}
	if abs != base && !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes photo base directory")
	}
	return abs, nil
}

type spaFileSystem struct {
	root http.FileSystem
}

func (fs *spaFileSystem) Open(name string) (http.File, error) {
	f, err := fs.root.Open(name)
	if os.IsNotExist(err) {
		return fs.root.Open("index.html")
	}
	return f, err
}

func main() {
	devMode := flag.Bool("dev", false, "Run in development mode (do not serve static files)")
	flag.Parse()

	userHomeDir, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("Failed to get user home directory: %v", err)
	}
	photoBaseDir = filepath.Join(userHomeDir, "Pictures", "photos")
	thumbnailCacheDir = filepath.Join(photoBaseDir, ".thumbnails")

	if err := os.MkdirAll(photoBaseDir, 0755); err != nil {
		log.Fatalf("Failed to create photo base directory: %v", err)
	}
	if err := os.MkdirAll(thumbnailCacheDir, 0755); err != nil {
		log.Fatalf("Failed to create thumbnail cache directory: %v", err)
	}

	http.HandleFunc("/api/directories", corsHandler(listDirectoriesHandler))
	http.HandleFunc("/api/photos", corsHandler(getPhotosHandler))
	http.HandleFunc("/api/save", corsHandler(saveSelectedPhotosHandler))
	http.HandleFunc("/api/import", corsHandler(importFromUSBHandler))
	http.HandleFunc("/api/import-preview", corsHandler(importPreviewHandler))
	http.HandleFunc("/api/export-raw", corsHandler(exportRawFilesHandler))
	http.HandleFunc("/api/export-raw-single", corsHandler(exportRawSingleFileHandler))
	http.HandleFunc("/api/export-status", corsHandler(exportStatusHandler))
	http.HandleFunc("/api/selected-photos", corsHandler(getSelectedPhotosHandler))
	http.HandleFunc("/api/delete-imported", corsHandler(deleteImportedHandler))
	http.HandleFunc("/api/delete-photos", corsHandler(deletePhotosHandler))
	http.HandleFunc("/api/rename-directory", corsHandler(renameDirectoryHandler))
	http.HandleFunc("/api/photo-metadata", corsHandler(photoMetadataHandler))
	http.HandleFunc("/photos/", corsHandler(servePhotoHandler))
	http.HandleFunc("/thumbnail/", corsHandler(serveThumbnailHandler))

	// Serve the frontend only if not in dev mode
	if !*devMode {
		fs, err := fs.Sub(frontend, "frontend/build")
		if err != nil {
			log.Fatalf("Failed to create sub file system: %v", err)
		}
		http.Handle("/", http.FileServer(&spaFileSystem{http.FS(fs)}))
	} else {
		log.Println("Running in dev mode. Frontend not served at root. Access via localhost:3000")
	}

	log.Println("Starting server on :5001")
	if err := http.ListenAndServe(":5001", nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func corsHandler(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		h(w, r)
	}
}

func listDirectoriesHandler(w http.ResponseWriter, r *http.Request) {
	files, err := ioutil.ReadDir(photoBaseDir)
	if err != nil {
		http.Error(w, "Failed to read photo base directory", http.StatusInternalServerError)
		return
	}

	var dirs []string
	for _, file := range files {
		if file.IsDir() && file.Name() != ".thumbnails" {
			dirs = append(dirs, file.Name())
		}
	}

	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dirs)
}

// validDirName reports whether name is usable as a photo session directory
// name: a single path segment that is not hidden (which also excludes the
// .thumbnails cache directory).
func validDirName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\`) && !strings.HasPrefix(name, ".")
}

func renameDirectoryHandler(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Directory string `json:"directory"`
		NewName   string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	newName := strings.TrimSpace(data.NewName)
	if !validDirName(data.Directory) || !validDirName(newName) {
		http.Error(w, "Invalid directory name", http.StatusBadRequest)
		return
	}
	if newName == data.Directory {
		http.Error(w, "New name is the same as the current name", http.StatusBadRequest)
		return
	}

	oldPath, err := safePhotoPath(data.Directory)
	if err != nil {
		http.Error(w, "Invalid directory", http.StatusBadRequest)
		return
	}
	newPath, err := safePhotoPath(newName)
	if err != nil {
		http.Error(w, "Invalid new directory name", http.StatusBadRequest)
		return
	}

	if info, err := os.Stat(oldPath); err != nil || !info.IsDir() {
		http.Error(w, "Directory not found", http.StatusNotFound)
		return
	}
	if _, err := os.Stat(newPath); err == nil {
		http.Error(w, "A directory with that name already exists", http.StatusConflict)
		return
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		log.Printf("Failed to rename directory %s to %s: %v", data.Directory, newName, err)
		http.Error(w, "Failed to rename directory", http.StatusInternalServerError)
		return
	}

	// Move the thumbnail cache along with the directory so existing thumbnails
	// stay valid and never need to be regenerated after a rename.
	oldThumbs := filepath.Join(thumbnailCacheDir, data.Directory)
	newThumbs := filepath.Join(thumbnailCacheDir, newName)
	if err := os.Rename(oldThumbs, newThumbs); err != nil && !os.IsNotExist(err) {
		log.Printf("Failed to move thumbnail cache from %s to %s: %v", oldThumbs, newThumbs, err)
	}

	log.Printf("Renamed directory %s to %s", data.Directory, newName)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message":       "Renamed '" + data.Directory + "' to '" + newName + "'",
		"new_directory": newName,
	})
}

func getPhotosHandler(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	if directory == "" {
		http.Error(w, "Missing 'directory' query parameter", http.StatusBadRequest)
		return
	}

	targetDir, err := safePhotoPath(directory)
	if err != nil {
		http.Error(w, "Invalid directory", http.StatusBadRequest)
		return
	}
	files, err := ioutil.ReadDir(targetDir)
	if err != nil {
		http.Error(w, "Failed to read photo directory", http.StatusInternalServerError)
		return
	}

	var photos []string
	var rawFiles []string
	for _, file := range files {
		if !file.IsDir() && !strings.HasPrefix(file.Name(), "._") {
			lowerName := strings.ToLower(file.Name())
			if strings.HasSuffix(lowerName, ".png") || strings.HasSuffix(lowerName, ".jpg") || strings.HasSuffix(lowerName, ".jpeg") || strings.HasSuffix(lowerName, ".gif") {
				photos = append(photos, file.Name())
			} else if isRawFile(file.Name()) {
				rawFiles = append(rawFiles, file.Name())
			}
		}
	}

	// If the folder contains only RAW files (no viewable images), expose the RAWs directly.
	if len(photos) == 0 {
		photos = rawFiles
	}

	sort.Strings(photos)

	// Start async thumbnail generation for this directory
	if len(photos) > 0 {
		go func() {
			log.Printf("Starting background thumbnail generation for directory: %s (%d photos)", directory, len(photos))
			preGenerateThumbnails(directory, photos)
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(photos)
}

func getSelectedPhotosHandler(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	if directory == "" {
		http.Error(w, "Missing 'directory' query parameter", http.StatusBadRequest)
		return
	}

	selectedDir, err := safePhotoPath(directory, "selected")
	if err != nil {
		http.Error(w, "Invalid directory", http.StatusBadRequest)
		return
	}
	files, err := ioutil.ReadDir(selectedDir)
	if err != nil {
		// If the directory doesn't exist, it just means no photos have been selected yet.
		// Return an empty list.
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]string{})
			return
		}
		http.Error(w, "Failed to read selected photo directory", http.StatusInternalServerError)
		return
	}

	var photos []string
	var rawFiles []string
	for _, file := range files {
		if !file.IsDir() && !strings.HasPrefix(file.Name(), "._") {
			lowerName := strings.ToLower(file.Name())
			if strings.HasSuffix(lowerName, ".png") || strings.HasSuffix(lowerName, ".jpg") || strings.HasSuffix(lowerName, ".jpeg") || strings.HasSuffix(lowerName, ".gif") {
				photos = append(photos, file.Name())
			} else if isRawFile(file.Name()) {
				rawFiles = append(rawFiles, file.Name())
			}
		}
	}

	if len(photos) == 0 {
		photos = rawFiles
	}

	sort.Strings(photos)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(photos)
}

func saveSelectedPhotosHandler(w http.ResponseWriter, r *http.Request) {
	var data struct {
		SelectedFiles []string `json:"selected_files"`
		Directory     string   `json:"directory"`
	}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if len(data.SelectedFiles) == 0 || data.Directory == "" {
		http.Error(w, "Missing 'selected_files' or 'directory' in request", http.StatusBadRequest)
		return
	}

	sourceDir, err := safePhotoPath(data.Directory)
	if err != nil {
		http.Error(w, "Invalid directory", http.StatusBadRequest)
		return
	}
	destinationDir := filepath.Join(sourceDir, "selected")

	if err := os.MkdirAll(destinationDir, 0755); err != nil {
		http.Error(w, "Failed to create destination directory", http.StatusInternalServerError)
		return
	}

	for _, filename := range data.SelectedFiles {
		if strings.Contains(filename, "..") || strings.ContainsAny(filename, `/\`) {
			log.Printf("Skipping invalid filename: %s", filename)
			continue
		}
		sourcePath := filepath.Join(sourceDir, filename)
		destinationPath := filepath.Join(destinationDir, filename)

		sourceFile, err := os.Open(sourcePath)
		if err != nil {
			log.Printf("Failed to open source file: %v", err)
			continue
		}
		defer sourceFile.Close()

		destinationFile, err := os.Create(destinationPath)
		if err != nil {
			log.Printf("Failed to create destination file: %v", err)
			continue
		}
		defer destinationFile.Close()

		if _, err := io.Copy(destinationFile, sourceFile); err != nil {
			log.Printf("Failed to copy file: %v", err)
			continue
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "Successfully copied " + strconv.Itoa(len(data.SelectedFiles)) + " files to '" + destinationDir + "'",
	})
}

func buildImportedFilesSet() map[string]bool {
	importedFiles := make(map[string]bool)

	dirs, err := ioutil.ReadDir(photoBaseDir)
	if err != nil {
		return importedFiles
	}

	for _, dir := range dirs {
		if dir.IsDir() && dir.Name() != ".thumbnails" {
			dirPath := filepath.Join(photoBaseDir, dir.Name())
			files, err := ioutil.ReadDir(dirPath)
			if err != nil {
				continue
			}

			for _, file := range files {
				if !file.IsDir() {
					importedFiles[file.Name()] = true
				}
			}
		}
	}

	return importedFiles
}

func importFromUSBHandler(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Since            string `json:"since"`
		Until            string `json:"until"`
		SkipDuplicates   bool   `json:"skip_duplicates"`
		TargetDirectory  string `json:"target_directory"`
		NewDirectoryName string `json:"new_directory_name"`
		ImportVideos     bool   `json:"import_videos"`
		ImportRaws       bool   `json:"import_raws"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil && err != io.EOF {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var sinceDate time.Time
	var untilDate time.Time
	var err error
	if data.Since != "" {
		sinceDate, err = time.Parse("2006-01-02", data.Since)
		if err != nil {
			http.Error(w, "Invalid date format. Please use YYYY-MM-DD.", http.StatusBadRequest)
			return
		}
	}
	if data.Until != "" {
		untilDate, err = time.Parse("2006-01-02", data.Until)
		if err != nil {
			http.Error(w, "Invalid date format. Please use YYYY-MM-DD.", http.StatusBadRequest)
			return
		}
		// Make until date inclusive by adding one day
		untilDate = untilDate.AddDate(0, 0, 1)
	}

	usbMountPoint := findUSBMountPoint()
	if usbMountPoint == "" {
		http.Error(w, "USB device with a camera DCIM directory (e.g. 100CANON, 100OLYMP) not found. Is it connected?", http.StatusNotFound)
		return
	}

	cameraDirs := findCameraDirectories(usbMountPoint)
	if len(cameraDirs) == 0 {
		http.Error(w, "Could not find a supported camera DCIM directory on USB device", http.StatusNotFound)
		return
	}

	// Determine destination directory: use target if specified, otherwise create new timestamped directory
	var destinationDir string
	var isNewBatch bool
	if data.TargetDirectory != "" {
		destinationDir, err = safePhotoPath(data.TargetDirectory)
		if err != nil {
			http.Error(w, "Invalid target directory", http.StatusBadRequest)
			return
		}
		isNewBatch = false
		// Verify target directory exists
		if _, err := os.Stat(destinationDir); os.IsNotExist(err) {
			http.Error(w, "Target directory does not exist", http.StatusBadRequest)
			return
		}
	} else {
		// New batch: use the client-supplied folder name when given (a single
		// folder level under photoBaseDir), otherwise default to a timestamp.
		name := strings.TrimSpace(data.NewDirectoryName)
		if name == "" {
			name = time.Now().Format("2006-01-02_15-04-05")
		}
		if strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) {
			http.Error(w, "Invalid folder name: must not start with '.' or contain path separators", http.StatusBadRequest)
			return
		}
		destinationDir, err = safePhotoPath(name)
		if err != nil {
			http.Error(w, "Invalid folder name", http.StatusBadRequest)
			return
		}
		if _, err := os.Stat(destinationDir); err == nil {
			http.Error(w, "A folder named '"+name+"' already exists. Use 'Add to current batch' to import into an existing folder.", http.StatusBadRequest)
			return
		}
		isNewBatch = true
	}

	destinationDirCreated := !isNewBatch // If adding to existing, directory already exists

	// Read files from all camera DCIM directories, tracking which directory each file came from
	type fileWithDir struct {
		file os.FileInfo
		dir  string
	}
	var allFiles []fileWithDir
	for _, cameraDir := range cameraDirs {
		sourceDir := filepath.Join(usbMountPoint, "DCIM", cameraDir)
		files, err := ioutil.ReadDir(sourceDir)
		if err != nil {
			log.Printf("Failed to read directory %s: %v", sourceDir, err)
			continue
		}
		for _, file := range files {
			allFiles = append(allFiles, fileWithDir{file: file, dir: cameraDir})
		}
	}

	if len(allFiles) == 0 {
		http.Error(w, "No files found in camera DCIM directories", http.StatusNotFound)
		return
	}

	// Build set of already imported files once (if skip duplicates is enabled)
	var importedFiles map[string]bool
	if data.SkipDuplicates {
		importedFiles = buildImportedFilesSet()
		log.Printf("Skip duplicates enabled: found %d already imported files", len(importedFiles))
	}

	// Pre-pass: determine exactly which files will be copied so we can report a
	// total up front and stream per-file progress during the copy pass.
	type fileToCopy struct {
		src      string
		destName string
		isMedia  bool // jpg or raw — included in thumbnail generation
	}
	var toCopy []fileToCopy
	skippedDuplicates := 0
	for _, fileEntry := range allFiles {
		file := fileEntry.file
		if file.IsDir() || strings.HasPrefix(file.Name(), "._") {
			continue
		}
		lowerName := strings.ToLower(file.Name())
		// Process .jpg files always, and .mp4/.raw files only if enabled
		isJpg := strings.HasSuffix(lowerName, ".jpg")
		isMp4 := strings.HasSuffix(lowerName, ".mp4")
		isRaw := isRawFile(file.Name())

		if !isJpg && (!isMp4 || !data.ImportVideos) && (!isRaw || !data.ImportRaws) {
			continue
		}

		sourceDir := filepath.Join(usbMountPoint, "DCIM", fileEntry.dir)
		sourceFile := filepath.Join(sourceDir, file.Name())

		if !sinceDate.IsZero() || !untilDate.IsZero() {
			fileInfo, err := os.Stat(sourceFile)
			if err != nil {
				log.Printf("Failed to get file info: %v", err)
				continue
			}
			modTime := fileInfo.ModTime()
			if !sinceDate.IsZero() && modTime.Before(sinceDate) {
				continue
			}
			if !untilDate.IsZero() && !modTime.Before(untilDate) {
				continue
			}
		}

		dirPrefix := getDCIMPrefix(fileEntry.dir)
		destFilename := file.Name()
		if dirPrefix != "" {
			destFilename = dirPrefix + "_" + file.Name()
		}

		// Check if file has already been imported to any directory (O(1) lookup)
		if data.SkipDuplicates && importedFiles[destFilename] {
			skippedDuplicates++
			continue
		}

		// Skip if the file already exists in an existing destination directory.
		if destinationDirCreated {
			if _, err := os.Stat(filepath.Join(destinationDir, destFilename)); err == nil {
				continue
			}
		}

		toCopy = append(toCopy, fileToCopy{src: sourceFile, destName: destFilename, isMedia: isJpg || isRaw})
	}

	// Everything below streams newline-delimited JSON (NDJSON) progress events so
	// the client can show a live progress bar. Once the first line is written the
	// HTTP status is fixed at 200, so all hard failures above use http.Error.
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

	dirName := filepath.Base(destinationDir)
	total := len(toCopy)

	// Nothing to copy: report why and stop (no directory is created).
	if total == 0 {
		var message string
		if !sinceDate.IsZero() || !untilDate.IsZero() {
			message = "No new files found in the selected date range"
		} else if skippedDuplicates > 0 {
			message = "All " + strconv.Itoa(skippedDuplicates) + " files have already been imported."
		} else {
			message = "No files found to import."
		}
		emit(map[string]interface{}{
			"type":               "done",
			"message":            message,
			"new_directory":      nil,
			"copied":             0,
			"skipped_duplicates": skippedDuplicates,
		})
		return
	}

	// Create the destination directory now that we know files will be copied.
	if !destinationDirCreated {
		if err := os.MkdirAll(destinationDir, 0755); err != nil {
			log.Printf("Failed to create destination directory: %v", err)
			emit(map[string]interface{}{"type": "error", "message": "Could not create destination directory"})
			return
		}
		destinationDirCreated = true
	}

	emit(map[string]interface{}{"type": "start", "total": total})

	// Throttle progress events to at most ~100 over the whole import.
	step := total / 100
	if step < 1 {
		step = 1
	}

	copiedCount := 0
	var copiedFiles []string
	for _, item := range toCopy {
		if err := copyFile(item.src, filepath.Join(destinationDir, item.destName)); err != nil {
			log.Printf("Failed to copy %s: %v", item.src, err)
			continue
		}
		copiedCount++
		if item.isMedia {
			copiedFiles = append(copiedFiles, item.destName)
		}
		if copiedCount == total || copiedCount%step == 0 {
			emit(map[string]interface{}{"type": "progress", "copied": copiedCount, "total": total})
		}
	}

	// Start async thumbnail generation for imported photos
	go func() {
		log.Printf("Starting background thumbnail generation for imported directory: %s (%d photos)", dirName, len(copiedFiles))
		preGenerateThumbnails(dirName, copiedFiles)
	}()

	message := "Successfully copied " + strconv.Itoa(copiedCount) + " new files"
	if !isNewBatch {
		message += " to " + dirName
	}
	message += "."
	if skippedDuplicates > 0 {
		message += " Skipped " + strconv.Itoa(skippedDuplicates) + " already imported."
	}

	var newDirectory interface{}
	if isNewBatch {
		newDirectory = dirName
	} else {
		newDirectory = nil
	}
	emit(map[string]interface{}{
		"type":               "done",
		"message":            message,
		"new_directory":      newDirectory,
		"copied":             copiedCount,
		"skipped_duplicates": skippedDuplicates,
	})
}

// copyFile copies a single file from src to dst, closing both handles before it
// returns. Using this (rather than inline defers inside a copy loop) keeps at
// most one source/destination file descriptor open at a time during an import.
func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	if _, err := io.Copy(destination, source); err != nil {
		return err
	}
	return nil
}

func importPreviewHandler(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Since           string `json:"since"`
		Until           string `json:"until"`
		SkipDuplicates  bool   `json:"skip_duplicates"`
		TargetDirectory string `json:"target_directory"`
		ImportVideos    bool   `json:"import_videos"`
		ImportRaws      bool   `json:"import_raws"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil && err != io.EOF {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var sinceDate time.Time
	var untilDate time.Time
	var err error
	if data.Since != "" {
		sinceDate, err = time.Parse("2006-01-02", data.Since)
		if err != nil {
			http.Error(w, "Invalid date format. Please use YYYY-MM-DD.", http.StatusBadRequest)
			return
		}
	}
	if data.Until != "" {
		untilDate, err = time.Parse("2006-01-02", data.Until)
		if err != nil {
			http.Error(w, "Invalid date format. Please use YYYY-MM-DD.", http.StatusBadRequest)
			return
		}
		// Make until date inclusive by adding one day
		untilDate = untilDate.AddDate(0, 0, 1)
	}

	usbMountPoint := findUSBMountPoint()
	if usbMountPoint == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"total_files":     0,
			"files_to_import": 0,
			"files_to_skip":   0,
			"usb_connected":   false,
			"error":           "USB device with a camera DCIM directory not found",
		})
		return
	}

	cameraDirs := findCameraDirectories(usbMountPoint)
	if len(cameraDirs) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"total_files":     0,
			"files_to_import": 0,
			"files_to_skip":   0,
			"usb_connected":   true,
			"error":           "Could not find supported camera directories on USB device",
		})
		return
	}

	// Determine destination directory for duplicate checking
	var destinationDir string
	if data.TargetDirectory != "" {
		destinationDir, err = safePhotoPath(data.TargetDirectory)
		if err != nil {
			http.Error(w, "Invalid target directory", http.StatusBadRequest)
			return
		}
		// Verify target directory exists
		if _, err := os.Stat(destinationDir); os.IsNotExist(err) {
			http.Error(w, "Target directory does not exist", http.StatusBadRequest)
			return
		}
	}

	// Read files from all camera DCIM directories
	type fileWithDir struct {
		file os.FileInfo
		dir  string
	}
	var allFiles []fileWithDir
	for _, cameraDir := range cameraDirs {
		sourceDir := filepath.Join(usbMountPoint, "DCIM", cameraDir)
		files, err := ioutil.ReadDir(sourceDir)
		if err != nil {
			log.Printf("Failed to read directory %s: %v", sourceDir, err)
			continue
		}
		for _, file := range files {
			allFiles = append(allFiles, fileWithDir{file: file, dir: cameraDir})
		}
	}

	// Build set of already imported files once (if skip duplicates is enabled)
	var importedFiles map[string]bool
	if data.SkipDuplicates {
		importedFiles = buildImportedFilesSet()
	}

	totalFiles := 0
	filesToImport := 0
	skippedDuplicates := 0
	skippedByDate := 0
	skippedVideos := 0
	skippedRaws := 0
	// dailyBreakdown maps "YYYY-MM-DD" -> count of files that will be imported that day
	dailyBreakdown := make(map[string]int)

	for _, fileEntry := range allFiles {
		file := fileEntry.file
		if !file.IsDir() && !strings.HasPrefix(file.Name(), "._") {
			lowerName := strings.ToLower(file.Name())
			isJpg := strings.HasSuffix(lowerName, ".jpg")
			isMp4 := strings.HasSuffix(lowerName, ".mp4")
			isRaw := isRawFile(file.Name())

			// Count all potential files
			if isJpg || isMp4 || isRaw {
				totalFiles++
			}

			// Skip if not jpg and not importing videos/raws
			if !isJpg && (!isMp4 || !data.ImportVideos) && (!isRaw || !data.ImportRaws) {
				if isMp4 {
					skippedVideos++
				}
				if isRaw {
					skippedRaws++
				}
				continue
			}

			sourceDir := filepath.Join(usbMountPoint, "DCIM", fileEntry.dir)
			sourceFile := filepath.Join(sourceDir, file.Name())

			// Check date filter (range)
			var modTime time.Time
			if !sinceDate.IsZero() || !untilDate.IsZero() {
				fileInfo, err := os.Stat(sourceFile)
				if err != nil {
					skippedByDate++
					continue
				}
				modTime = fileInfo.ModTime()
				if !sinceDate.IsZero() && modTime.Before(sinceDate) {
					skippedByDate++
					continue
				}
				if !untilDate.IsZero() && !modTime.Before(untilDate) {
					skippedByDate++
					continue
				}
			} else {
				// Still need modTime for daily breakdown
				if fileInfo, err := os.Stat(sourceFile); err == nil {
					modTime = fileInfo.ModTime()
				}
			}

			dirPrefix := getDCIMPrefix(fileEntry.dir)
			destFilename := file.Name()
			if dirPrefix != "" {
				destFilename = dirPrefix + "_" + file.Name()
			}

			// Check if already imported
			if data.SkipDuplicates && importedFiles[destFilename] {
				skippedDuplicates++
				continue
			}

			// Check if file already exists in target destination
			if destinationDir != "" {
				destinationFile := filepath.Join(destinationDir, destFilename)
				if _, err := os.Stat(destinationFile); err == nil {
					skippedDuplicates++
					continue
				}
			}

			filesToImport++
			if !modTime.IsZero() {
				dateKey := modTime.Format("2006-01-02")
				dailyBreakdown[dateKey]++
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"total_files":        totalFiles,
		"files_to_import":    filesToImport,
		"skipped_duplicates": skippedDuplicates,
		"skipped_by_date":    skippedByDate,
		"skipped_videos":     skippedVideos,
		"skipped_raws":       skippedRaws,
		"usb_connected":      true,
		"daily_breakdown":    dailyBreakdown,
	})
}

func exportRawFilesHandler(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Directory string `json:"directory"`
	}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if data.Directory == "" {
		http.Error(w, "Missing 'directory' in request", http.StatusBadRequest)
		return
	}

	// Find USB/SD card mount point
	usbMountPoint := findUSBMountPoint()
	if usbMountPoint == "" {
		http.Error(w, "USB device with a camera DCIM directory (e.g. 100CANON, 100OLYMP) not found. Is the SD card connected?", http.StatusNotFound)
		return
	}

	if len(findCameraDirectories(usbMountPoint)) == 0 {
		http.Error(w, "Could not find a supported camera DCIM directory on USB device", http.StatusNotFound)
		return
	}

	sourceDir, err := safePhotoPath(data.Directory)
	if err != nil {
		http.Error(w, "Invalid directory", http.StatusBadRequest)
		return
	}
	selectedDir := filepath.Join(sourceDir, "selected")
	rawDestDir := filepath.Join(selectedDir, "raw")

	// Check if selected directory exists and has files
	selectedFiles, err := ioutil.ReadDir(selectedDir)
	if err != nil {
		http.Error(w, "Selected directory not found or empty", http.StatusNotFound)
		return
	}

	// Filter for JPEG files in selected directory
	var jpegFiles []string
	for _, file := range selectedFiles {
		if !file.IsDir() {
			lowerName := strings.ToLower(file.Name())
			if strings.HasSuffix(lowerName, ".jpg") || strings.HasSuffix(lowerName, ".jpeg") {
				jpegFiles = append(jpegFiles, file.Name())
			}
		}
	}

	if len(jpegFiles) == 0 {
		http.Error(w, "No JPEG files found in selected directory", http.StatusNotFound)
		return
	}

	// Create raw destination directory
	if err := os.MkdirAll(rawDestDir, 0755); err != nil {
		http.Error(w, "Failed to create raw destination directory", http.StatusInternalServerError)
		return
	}

	copiedCount := 0
	skippedCount := 0
	notFoundCount := 0

	for _, jpegFile := range jpegFiles {
		ext := filepath.Ext(jpegFile)
		baseName := strings.TrimSuffix(jpegFile, ext)

		prefix, originalBaseName := splitPrefixedFilename(baseName)

		// Skip if a raw file (any supported extension) is already at destination
		if rawAlreadyExported(rawDestDir, baseName) {
			skippedCount++
			continue
		}

		rawSourcePath, rawExt, found := findRawForJPG(usbMountPoint, prefix, originalBaseName)
		if !found {
			log.Printf("Raw file not found on SD card for %s", originalBaseName)
			notFoundCount++
			continue
		}

		rawDestPath := filepath.Join(rawDestDir, baseName+rawExt)

		// Copy the raw file from SD card
		source, err := os.Open(rawSourcePath)
		if err != nil {
			log.Printf("Failed to open source raw file: %v", err)
			notFoundCount++
			continue
		}
		defer source.Close()

		destination, err := os.Create(rawDestPath)
		if err != nil {
			log.Printf("Failed to create destination raw file: %v", err)
			continue
		}
		defer destination.Close()

		if _, err := io.Copy(destination, source); err != nil {
			log.Printf("Failed to copy raw file: %v", err)
			continue
		}
		copiedCount++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":        "Raw file export complete",
		"copied":         copiedCount,
		"skipped":        skippedCount,
		"not_found":      notFoundCount,
		"total_selected": len(jpegFiles),
	})
}

func exportRawSingleFileHandler(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Directory string `json:"directory"`
		Filename  string `json:"filename"`
	}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if data.Directory == "" || data.Filename == "" {
		http.Error(w, "Missing 'directory' or 'filename' in request", http.StatusBadRequest)
		return
	}

	// Find USB/SD card mount point
	usbMountPoint := findUSBMountPoint()
	if usbMountPoint == "" {
		http.Error(w, "USB device with a camera DCIM directory (e.g. 100CANON, 100OLYMP) not found. Is the SD card connected?", http.StatusNotFound)
		return
	}

	sourceDir, err := safePhotoPath(data.Directory)
	if err != nil {
		http.Error(w, "Invalid directory", http.StatusBadRequest)
		return
	}
	if strings.Contains(data.Filename, "..") || strings.ContainsAny(data.Filename, `/\`) {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}
	selectedDir := filepath.Join(sourceDir, "selected")
	rawDestDir := filepath.Join(selectedDir, "raw")

	// Create raw destination directory
	if err := os.MkdirAll(rawDestDir, 0755); err != nil {
		http.Error(w, "Failed to create raw destination directory", http.StatusInternalServerError)
		return
	}

	// Get the base filename without extension
	ext := filepath.Ext(data.Filename)
	baseName := strings.TrimSuffix(data.Filename, ext)

	prefix, originalBaseName := splitPrefixedFilename(baseName)

	// Skip if a raw file (any supported extension) is already at destination
	if rawAlreadyExported(rawDestDir, baseName) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "Raw file already exported",
			"status":  "skipped",
		})
		return
	}

	rawSourcePath, rawExt, found := findRawForJPG(usbMountPoint, prefix, originalBaseName)
	if !found {
		http.Error(w, "Raw file not found on SD card", http.StatusNotFound)
		return
	}

	rawDestPath := filepath.Join(rawDestDir, baseName+rawExt)

	// Copy the raw file from SD card
	source, err := os.Open(rawSourcePath)
	if err != nil {
		log.Printf("Failed to open source raw file: %v", err)
		http.Error(w, "Failed to open source raw file", http.StatusInternalServerError)
		return
	}
	defer source.Close()

	destination, err := os.Create(rawDestPath)
	if err != nil {
		log.Printf("Failed to create destination raw file: %v", err)
		http.Error(w, "Failed to create destination raw file", http.StatusInternalServerError)
		return
	}
	defer destination.Close()

	if _, err := io.Copy(destination, source); err != nil {
		log.Printf("Failed to copy raw file: %v", err)
		http.Error(w, "Failed to copy raw file", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Raw file export complete",
		"status":  "copied",
	})
}

func exportStatusHandler(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	if directory == "" {
		http.Error(w, "Missing 'directory' query parameter", http.StatusBadRequest)
		return
	}

	sourceDir, err := safePhotoPath(directory)
	if err != nil {
		http.Error(w, "Invalid directory", http.StatusBadRequest)
		return
	}
	selectedDir := filepath.Join(sourceDir, "selected")
	rawDir := filepath.Join(selectedDir, "raw")

	// Count JPEG files in selected directory
	selectedCount := 0
	var jpegFiles []string
	if files, err := ioutil.ReadDir(selectedDir); err == nil {
		for _, file := range files {
			if !file.IsDir() {
				lowerName := strings.ToLower(file.Name())
				if strings.HasSuffix(lowerName, ".jpg") || strings.HasSuffix(lowerName, ".jpeg") {
					selectedCount++
					jpegFiles = append(jpegFiles, file.Name())
				}
			}
		}
	}

	// Count raw files in raw directory (any supported extension)
	rawCount := 0
	rawBaseSet := make(map[string]bool)
	if files, err := ioutil.ReadDir(rawDir); err == nil {
		for _, file := range files {
			if !file.IsDir() && isRawFile(file.Name()) {
				rawCount++
				base := strings.TrimSuffix(file.Name(), filepath.Ext(file.Name()))
				rawBaseSet[strings.ToLower(base)] = true
			}
		}
	}

	// Calculate missing raw files
	missingCount := 0
	for _, jpegFile := range jpegFiles {
		base := strings.TrimSuffix(jpegFile, filepath.Ext(jpegFile))
		if !rawBaseSet[strings.ToLower(base)] {
			missingCount++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"selected_count": selectedCount,
		"raw_count":      rawCount,
		"missing_count":  missingCount,
	})
}

func deleteImportedHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Find USB/SD card mount point
	usbMountPoint := findUSBMountPoint()
	if usbMountPoint == "" {
		http.Error(w, "USB device with a camera DCIM directory (e.g. 100CANON, 100OLYMP) not found. Is it connected?", http.StatusNotFound)
		return
	}

	cameraDirs := findCameraDirectories(usbMountPoint)
	if len(cameraDirs) == 0 {
		http.Error(w, "Could not find a supported camera DCIM directory on USB device", http.StatusNotFound)
		return
	}

	// Build set of imported files using the same logic as the import handler
	importedFiles := buildImportedFilesSet()
	log.Printf("Delete imported: found %d already imported files", len(importedFiles))

	deletedCount := 0
	deletedRawCount := 0
	notFoundCount := 0
	errorCount := 0

	// Process files from all camera DCIM directories
	for _, cameraDir := range cameraDirs {
		sourceDir := filepath.Join(usbMountPoint, "DCIM", cameraDir)
		files, err := ioutil.ReadDir(sourceDir)
		if err != nil {
			log.Printf("Failed to read directory %s: %v", sourceDir, err)
			continue
		}

		for _, file := range files {
			if !file.IsDir() && !strings.HasPrefix(file.Name(), "._") {
				lowerName := strings.ToLower(file.Name())
				// Process both .jpg and .mp4 files
				isJpg := strings.HasSuffix(lowerName, ".jpg")
				isMp4 := strings.HasSuffix(lowerName, ".mp4")
				if !isJpg && !isMp4 {
					continue
				}

				dirPrefix := getDCIMPrefix(cameraDir)
				destFilename := file.Name()
				if dirPrefix != "" {
					destFilename = dirPrefix + "_" + file.Name()
				}

				// Only delete files that are in the imported set
				if importedFiles[destFilename] {
					filePath := filepath.Join(sourceDir, file.Name())
					if err := os.Remove(filePath); err == nil {
						deletedCount++
						log.Printf("Deleted imported file: %s", file.Name())

						// If it's a JPG, also try to delete the associated RAW file
						if isJpg {
							baseName := strings.TrimSuffix(file.Name(), filepath.Ext(file.Name()))
							if brand := detectCameraBrand(cameraDir); brand != nil {
								rawFilePath := filepath.Join(sourceDir, baseName+brand.rawExt)
								if err := os.Remove(rawFilePath); err == nil {
									deletedRawCount++
									log.Printf("Deleted associated RAW file: %s", baseName+brand.rawExt)
								}
							}
						}
					} else {
						if os.IsNotExist(err) {
							notFoundCount++
						} else {
							log.Printf("Failed to delete file %s: %v", filePath, err)
							errorCount++
						}
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":     "Delete operation complete",
		"deleted":     deletedCount,
		"deleted_raw": deletedRawCount,
		"not_found":   notFoundCount,
		"errors":      errorCount,
		"total_found": deletedCount + deletedRawCount + notFoundCount + errorCount,
	})
}

func deletePhotosHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
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

	if data.Directory == "" {
		http.Error(w, "Missing 'directory' in request", http.StatusBadRequest)
		return
	}

	if len(data.Files) == 0 {
		http.Error(w, "No files specified for deletion", http.StatusBadRequest)
		return
	}

	// Security: ensure the target directory stays within the photo base directory.
	targetDir, err := safePhotoPath(data.Directory)
	if err != nil {
		http.Error(w, "Invalid directory", http.StatusBadRequest)
		return
	}
	deletedCount := 0
	notFoundCount := 0
	errorCount := 0

	for _, filename := range data.Files {
		// Security: ensure filename doesn't contain path traversal
		if strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
			log.Printf("Skipping invalid filename: %s", filename)
			errorCount++
			continue
		}

		filePath := filepath.Join(targetDir, filename)

		if err := os.Remove(filePath); err != nil {
			if os.IsNotExist(err) {
				notFoundCount++
			} else {
				log.Printf("Failed to delete file %s: %v", filePath, err)
				errorCount++
			}
		} else {
			deletedCount++
			log.Printf("Deleted file: %s", filename)

			// Also try to delete thumbnail if it exists
			thumbnailPath := filepath.Join(thumbnailCacheDir, data.Directory, filename)
			if err := os.Remove(thumbnailPath); err != nil && !os.IsNotExist(err) {
				log.Printf("Failed to delete thumbnail %s: %v", thumbnailPath, err)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":   "Delete operation complete",
		"deleted":   deletedCount,
		"not_found": notFoundCount,
		"errors":    errorCount,
	})
}

// findCameraDirectories returns DCIM subdirectories whose suffix matches a supported brand
// (e.g. 100CANON, 101CANON, 100OLYMP).
func findCameraDirectories(mountPoint string) []string {
	var dirs []string
	dcimPath := filepath.Join(mountPoint, "DCIM")
	files, err := ioutil.ReadDir(dcimPath)
	if err != nil {
		return dirs
	}

	re := regexp.MustCompile(`^[0-9]{3}`)
	for _, file := range files {
		if file.IsDir() && re.MatchString(file.Name()) && detectCameraBrand(file.Name()) != nil {
			dirs = append(dirs, file.Name())
		}
	}
	return dirs
}

func findCameraDirectory(mountPoint string) string {
	dirs := findCameraDirectories(mountPoint)
	if len(dirs) > 0 {
		return dirs[0]
	}
	return ""
}

// findRawForJPG locates the RAW file on the camera card matching the given JPG base name.
// It prefers a DCIM folder whose 3-digit prefix matches the JPG's prefix, and falls back to
// scanning all camera folders.
func findRawForJPG(mountPoint, prefix, originalBaseName string) (rawPath, rawExt string, found bool) {
	cameraDirs := findCameraDirectories(mountPoint)

	check := func(dir string) (string, string, bool) {
		brand := detectCameraBrand(dir)
		if brand == nil {
			return "", "", false
		}
		candidate := filepath.Join(mountPoint, "DCIM", dir, originalBaseName+brand.rawExt)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, brand.rawExt, true
		}
		return "", "", false
	}

	if prefix != "" {
		for _, dir := range cameraDirs {
			if !strings.HasPrefix(dir, prefix) {
				continue
			}
			if path, ext, ok := check(dir); ok {
				return path, ext, true
			}
		}
	}

	for _, dir := range cameraDirs {
		if path, ext, ok := check(dir); ok {
			return path, ext, true
		}
	}

	return "", "", false
}

func findUSBMountPoint() string {
	switch runtime.GOOS {
	case "darwin":
		volumesDir := "/Volumes"
		dirs, err := ioutil.ReadDir(volumesDir)
		if err != nil {
			return ""
		}
		for _, dir := range dirs {
			if dir.IsDir() {
				mountPoint := filepath.Join(volumesDir, dir.Name())
				if findCameraDirectory(mountPoint) != "" {
					return mountPoint
				}
			}
		}
	case "linux":
		mediaDir := filepath.Join("/media", os.Getenv("USER"))
		dirs, err := ioutil.ReadDir(mediaDir)
		if err != nil {
			return ""
		}
		for _, dir := range dirs {
			if dir.IsDir() {
				mountPoint := filepath.Join(mediaDir, dir.Name())
				if findCameraDirectory(mountPoint) != "" {
					return mountPoint
				}
			}
		}
	}
	return ""
}

// thumbnailLocks serializes generation of the same thumbnail. The import
// worker pool, the /api/photos worker pool, and on-demand /thumbnail/ requests
// can all race to generate the same file; without this, concurrent writers
// interleave on the same path and readers can be served a half-written JPEG.
var thumbnailLocks sync.Map

func generateThumbnail(directory, filename string) error {
	thumbnailDir := filepath.Join(thumbnailCacheDir, directory)
	thumbnailPath := filepath.Join(thumbnailDir, filename)

	lockAny, _ := thumbnailLocks.LoadOrStore(directory+"/"+filename, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	// Check if thumbnail already exists (re-checked under the lock so a
	// goroutine that waited on a concurrent generation returns immediately)
	if _, err := os.Stat(thumbnailPath); err == nil {
		return nil // Already exists
	}

	originalPhotoPath := filepath.Join(photoBaseDir, directory, filename)

	var img image.Image
	if isRawFile(filename) {
		jpegData, err := extractEmbeddedJPEG(originalPhotoPath)
		if err != nil {
			return fmt.Errorf("extracting embedded JPEG from %s: %w", filename, err)
		}
		img, err = jpeg.Decode(bytes.NewReader(jpegData))
		if err != nil {
			return fmt.Errorf("decoding embedded JPEG from %s: %w", filename, err)
		}
	} else {
		file, err := os.Open(originalPhotoPath)
		if err != nil {
			return err
		}
		defer file.Close()
		img, _, err = image.Decode(file)
		if err != nil {
			return err
		}
	}

	thumb := resize.Thumbnail(uint(thumbnailSize), uint(thumbnailSize), img, resize.Lanczos3)

	if err := os.MkdirAll(thumbnailDir, 0755); err != nil {
		return err
	}

	// Write to a temp file and rename so a concurrent /thumbnail/ request can
	// never observe (and serve) a partially written thumbnail.
	out, err := os.CreateTemp(thumbnailDir, "."+filename+".tmp")
	if err != nil {
		return err
	}
	if err := jpeg.Encode(out, thumb, nil); err != nil {
		out.Close()
		os.Remove(out.Name())
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(out.Name())
		return err
	}
	return os.Rename(out.Name(), thumbnailPath)
}

func preGenerateThumbnails(directory string, photos []string) {
	const numWorkers = 20
	var wg sync.WaitGroup
	photoChan := make(chan string, len(photos))

	// Start worker goroutines
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for filename := range photoChan {
				if err := generateThumbnail(directory, filename); err != nil {
					log.Printf("Failed to generate thumbnail for %s: %v", filename, err)
				}
			}
		}()
	}

	// Send photos to workers
	for _, photo := range photos {
		photoChan <- photo
	}
	close(photoChan)

	// Wait for all workers to complete
	wg.Wait()
	log.Printf("Completed thumbnail generation for directory: %s (%d photos)", directory, len(photos))
}

func photoMetadataHandler(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	photo := r.URL.Query().Get("photo")
	if directory == "" || photo == "" {
		http.Error(w, "Missing 'directory' or 'photo' query parameter", http.StatusBadRequest)
		return
	}
	photoPath, err := safePhotoPath(directory, photo)
	if err != nil {
		http.Error(w, "Invalid photo path", http.StatusBadRequest)
		return
	}

	// Files without EXIF (PNGs, screenshots, stripped JPEGs) are normal —
	// return an empty object so the frontend simply shows nothing.
	meta, err := extractPhotoMetadata(photoPath)
	if err != nil {
		meta = photoMetadata{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(meta)
}

func servePhotoHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/photos/"), "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid photo path", http.StatusBadRequest)
		return
	}
	directory := parts[0]
	filename := parts[1]
	photoPath, err := safePhotoPath(directory, filename)
	if err != nil {
		http.Error(w, "Invalid photo path", http.StatusBadRequest)
		return
	}

	if isRawFile(filename) {
		jpegData, err := extractEmbeddedJPEG(photoPath)
		if err != nil {
			http.Error(w, "Failed to extract preview from RAW file", http.StatusInternalServerError)
			log.Printf("Error extracting JPEG from RAW %s: %v", photoPath, err)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpegData)
		return
	}

	http.ServeFile(w, r, photoPath)
}

func serveThumbnailHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/thumbnail/"), "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid thumbnail path", http.StatusBadRequest)
		return
	}
	directory := parts[0]
	filename := parts[1]

	// Guard against traversal in the directory/filename segments.
	if strings.Contains(directory, "..") || strings.Contains(filename, "..") {
		http.Error(w, "Invalid thumbnail path", http.StatusBadRequest)
		return
	}

	thumbnailDir := filepath.Join(thumbnailCacheDir, directory)
	thumbnailPath := filepath.Join(thumbnailDir, filename)

	// Generate thumbnail on-demand if it doesn't exist
	if _, err := os.Stat(thumbnailPath); err != nil {
		if err := generateThumbnail(directory, filename); err != nil {
			http.Error(w, "Failed to generate thumbnail", http.StatusInternalServerError)
			log.Printf("Error generating thumbnail for %s/%s: %v", directory, filename, err)
			return
		}
	}

	// RAW thumbnails are JPEG bytes stored under the raw filename — set content type explicitly.
	if isRawFile(filename) {
		f, err := os.Open(thumbnailPath)
		if err != nil {
			http.Error(w, "Failed to serve thumbnail", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			http.Error(w, "Failed to stat thumbnail", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeContent(w, r, filename+".jpg", stat.ModTime(), f)
		return
	}

	http.ServeFile(w, r, thumbnailPath)
}

func getDCIMPrefix(dir string) string {
	if len(dir) >= 3 {
		prefix := dir[:3]
		if _, err := strconv.Atoi(prefix); err == nil {
			return prefix
		}
	}
	return ""
}

func splitPrefixedFilename(filename string) (prefix string, originalName string) {
	if len(filename) > 4 && filename[3] == '_' {
		p := filename[:3]
		if _, err := strconv.Atoi(p); err == nil {
			return p, filename[4:]
		}
	}
	return "", filename
}
