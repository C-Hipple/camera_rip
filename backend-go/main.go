package main

import (
	"embed"
	"encoding/json"
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
	"strings"
	"time"

	"github.com/nfnt/resize"
)

//go:embed all:frontend/build
var frontend embed.FS

var (
	photoBaseDir      string
	thumbnailCacheDir string
	thumbnailSize     = 200
)

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
	http.HandleFunc("/api/export-raw", corsHandler(exportRawFilesHandler))
	http.HandleFunc("/api/export-status", corsHandler(exportStatusHandler))
	http.HandleFunc("/photos/", corsHandler(servePhotoHandler))
	http.HandleFunc("/thumbnail/", corsHandler(serveThumbnailHandler))

	// Serve the frontend
	fs, err := fs.Sub(frontend, "frontend/build")
	if err != nil {
		log.Fatalf("Failed to create sub file system: %v", err)
	}
	http.Handle("/", http.FileServer(&spaFileSystem{http.FS(fs)}))

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

func getPhotosHandler(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	if directory == "" {
		http.Error(w, "Missing 'directory' query parameter", http.StatusBadRequest)
		return
	}

	targetDir := filepath.Join(photoBaseDir, directory)
	files, err := ioutil.ReadDir(targetDir)
	if err != nil {
		http.Error(w, "Failed to read photo directory", http.StatusInternalServerError)
		return
	}

	var photos []string
	for _, file := range files {
		if !file.IsDir() {
			lowerName := strings.ToLower(file.Name())
			if strings.HasSuffix(lowerName, ".png") || strings.HasSuffix(lowerName, ".jpg") || strings.HasSuffix(lowerName, ".jpeg") || strings.HasSuffix(lowerName, ".gif") {
				if !strings.HasPrefix(file.Name(), "._") {
					photos = append(photos, file.Name())
				}
			}
		}
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

	sourceDir := filepath.Join(photoBaseDir, data.Directory)
	destinationDir := filepath.Join(sourceDir, "selected")

	if err := os.MkdirAll(destinationDir, 0755); err != nil {
		http.Error(w, "Failed to create destination directory", http.StatusInternalServerError)
		return
	}

	for _, filename := range data.SelectedFiles {
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
		"message": "Successfully copied " + string(len(data.SelectedFiles)) + " files to '" + destinationDir + "'",
	})
}

func importFromUSBHandler(w http.ResponseWriter, r *http.Request) {
	var data struct {
		Since string `json:"since"`
	}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil && err != io.EOF {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var sinceDate time.Time
	var err error
	if data.Since != "" {
		sinceDate, err = time.Parse("2006-01-02", data.Since)
		if err != nil {
			http.Error(w, "Invalid date format. Please use YYYY-MM-DD.", http.StatusBadRequest)
			return
		}
	}

	usbMountPoint := findUSBMountPoint()
	if usbMountPoint == "" {
		http.Error(w, "USB device with 'DCIM/100CANON' directory not found. Is it connected?", http.StatusNotFound)
		return
	}

	sourceDir := filepath.Join(usbMountPoint, "DCIM", "100CANON")
	destinationDir := filepath.Join(photoBaseDir, time.Now().Format("2006-01-02_15-04-05"))

	if err := os.MkdirAll(destinationDir, 0755); err != nil {
		http.Error(w, "Could not create destination directory", http.StatusInternalServerError)
		return
	}

	files, err := ioutil.ReadDir(sourceDir)
	if err != nil {
		http.Error(w, "Failed to read source directory", http.StatusInternalServerError)
		return
	}

	copiedCount := 0
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(strings.ToLower(file.Name()), ".jpg") && !strings.HasPrefix(file.Name(), "._") {
			sourceFile := filepath.Join(sourceDir, file.Name())

			if !sinceDate.IsZero() {
				fileInfo, err := os.Stat(sourceFile)
				if err != nil {
					log.Printf("Failed to get file info: %v", err)
					continue
				}
				if fileInfo.ModTime().Before(sinceDate) {
					continue
				}
			}

			destinationFile := filepath.Join(destinationDir, file.Name())
			if _, err := os.Stat(destinationFile); err == nil {
				continue // Skip if file already exists
			}

			source, err := os.Open(sourceFile)
			if err != nil {
				log.Printf("Failed to open source file: %v", err)
				continue
			}
			defer source.Close()

			destination, err := os.Create(destinationFile)
			if err != nil {
				log.Printf("Failed to create destination file: %v", err)
				continue
			}
			defer destination.Close()

			if _, err := io.Copy(destination, source); err != nil {
				log.Printf("Failed to copy file: %v", err)
				continue
			}
			copiedCount++
		}
	}

	if copiedCount == 0 && !sinceDate.IsZero() {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message":       "No new photos found since " + data.Since,
			"new_directory": nil,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":       "Successfully copied " + string(copiedCount) + " new files.",
		"new_directory": filepath.Base(destinationDir),
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
		http.Error(w, "USB device with 'DCIM/100CANON' directory not found. Is the SD card connected?", http.StatusNotFound)
		return
	}

	sdCardDir := filepath.Join(usbMountPoint, "DCIM", "100CANON")
	sourceDir := filepath.Join(photoBaseDir, data.Directory)
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
		// Get the base filename without extension
		ext := filepath.Ext(jpegFile)
		baseName := strings.TrimSuffix(jpegFile, ext)
		rawFileName := baseName + ".CR3"

		// Look for raw file on SD card
		rawSourcePath := filepath.Join(sdCardDir, rawFileName)
		rawDestPath := filepath.Join(rawDestDir, rawFileName)

		// Check if raw file already exists at destination
		if _, err := os.Stat(rawDestPath); err == nil {
			skippedCount++
			continue
		}

		// Check if raw file exists on SD card
		if _, err := os.Stat(rawSourcePath); os.IsNotExist(err) {
			log.Printf("Raw file not found on SD card: %s", rawSourcePath)
			notFoundCount++
			continue
		}

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

func exportStatusHandler(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	if directory == "" {
		http.Error(w, "Missing 'directory' query parameter", http.StatusBadRequest)
		return
	}

	sourceDir := filepath.Join(photoBaseDir, directory)
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

	// Count CR3 files in raw directory
	rawCount := 0
	rawFileMap := make(map[string]bool)
	if files, err := ioutil.ReadDir(rawDir); err == nil {
		for _, file := range files {
			if !file.IsDir() {
				lowerName := strings.ToLower(file.Name())
				if strings.HasSuffix(lowerName, ".cr3") {
					rawCount++
					rawFileMap[file.Name()] = true
				}
			}
		}
	}

	// Calculate missing raw files
	missingCount := 0
	for _, jpegFile := range jpegFiles {
		ext := filepath.Ext(jpegFile)
		baseName := strings.TrimSuffix(jpegFile, ext)
		rawFileName := baseName + ".CR3"
		if !rawFileMap[rawFileName] {
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
				checkPath := filepath.Join(mountPoint, "DCIM", "100CANON")
				if _, err := os.Stat(checkPath); err == nil {
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
				checkPath := filepath.Join(mountPoint, "DCIM", "100CANON")
				if _, err := os.Stat(checkPath); err == nil {
					return mountPoint
				}
			}
		}
	}
	return ""
}

func servePhotoHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/photos/"), "/")
	if len(parts) < 2 {
		http.Error(w, "Invalid photo path", http.StatusBadRequest)
		return
	}
	directory := parts[0]
	filename := parts[1]
	photoPath := filepath.Join(photoBaseDir, directory, filename)
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

	thumbnailDir := filepath.Join(thumbnailCacheDir, directory)
	thumbnailPath := filepath.Join(thumbnailDir, filename)

	if _, err := os.Stat(thumbnailPath); err == nil {
		http.ServeFile(w, r, thumbnailPath)
		return
	}

	originalPhotoPath := filepath.Join(photoBaseDir, directory, filename)
	if _, err := os.Stat(originalPhotoPath); os.IsNotExist(err) {
		http.Error(w, "Original photo not found", http.StatusNotFound)
		return
	}

	file, err := os.Open(originalPhotoPath)
	if err != nil {
		http.Error(w, "Failed to open original photo", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		http.Error(w, "Failed to decode image", http.StatusInternalServerError)
		return
	}

	thumb := resize.Thumbnail(uint(thumbnailSize), uint(thumbnailSize), img, resize.Lanczos3)

	if err := os.MkdirAll(thumbnailDir, 0755); err != nil {
		http.Error(w, "Failed to create thumbnail directory", http.StatusInternalServerError)
		return
	}

	out, err := os.Create(thumbnailPath)
	if err != nil {
		http.Error(w, "Failed to create thumbnail file", http.StatusInternalServerError)
		return
	}
	defer out.Close()

	jpeg.Encode(out, thumb, nil)
	http.ServeFile(w, r, thumbnailPath)
}
