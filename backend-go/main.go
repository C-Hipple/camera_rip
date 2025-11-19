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
	"strconv"
	"strings"
	"sync"
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
	http.HandleFunc("/api/selected-photos", corsHandler(getSelectedPhotosHandler))
	http.HandleFunc("/api/delete-imported", corsHandler(deleteImportedHandler))
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

	selectedDir := filepath.Join(photoBaseDir, directory, "selected")
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
		Since           string `json:"since"`
		SkipDuplicates  bool   `json:"skip_duplicates"`
		TargetDirectory string `json:"target_directory"`
		ImportVideos    bool   `json:"import_videos"`
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
	
	// Determine destination directory: use target if specified, otherwise create new timestamped directory
	var destinationDir string
	var isNewBatch bool
	if data.TargetDirectory != "" {
		destinationDir = filepath.Join(photoBaseDir, data.TargetDirectory)
		isNewBatch = false
		// Verify target directory exists
		if _, err := os.Stat(destinationDir); os.IsNotExist(err) {
			http.Error(w, "Target directory does not exist", http.StatusBadRequest)
			return
		}
	} else {
		destinationDir = filepath.Join(photoBaseDir, time.Now().Format("2006-01-02_15-04-05"))
		isNewBatch = true
	}
	
	destinationDirCreated := !isNewBatch // If adding to existing, directory already exists

	files, err := ioutil.ReadDir(sourceDir)
	if err != nil {
		http.Error(w, "Failed to read source directory", http.StatusInternalServerError)
		return
	}

	// Build set of already imported files once (if skip duplicates is enabled)
	var importedFiles map[string]bool
	if data.SkipDuplicates {
		importedFiles = buildImportedFilesSet()
		log.Printf("Skip duplicates enabled: found %d already imported files", len(importedFiles))
	}

	copiedCount := 0
	skippedDuplicates := 0
	var copiedFiles []string
	for _, file := range files {
		if !file.IsDir() && !strings.HasPrefix(file.Name(), "._") {
			lowerName := strings.ToLower(file.Name())
			// Process .jpg files always, and .mp4 files only if import_videos is enabled
			isJpg := strings.HasSuffix(lowerName, ".jpg")
			isMp4 := strings.HasSuffix(lowerName, ".mp4")
			if !isJpg && (!isMp4 || !data.ImportVideos) {
				continue
			}

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

			// Check if file has already been imported to any directory (O(1) lookup)
			if data.SkipDuplicates && importedFiles[file.Name()] {
				skippedDuplicates++
				continue
			}

			// Create destination directory on first file to be copied
			if !destinationDirCreated {
				if err := os.MkdirAll(destinationDir, 0755); err != nil {
					log.Printf("Failed to create destination directory: %v", err)
					http.Error(w, "Could not create destination directory", http.StatusInternalServerError)
					return
				}
				destinationDirCreated = true
			}

			destinationFile := filepath.Join(destinationDir, file.Name())
			if _, err := os.Stat(destinationFile); err == nil {
				continue // Skip if file already exists in current destination
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
			// Only add image files to copiedFiles for thumbnail generation
			if isJpg {
				copiedFiles = append(copiedFiles, file.Name())
			}
		}
	}

	// Handle case where no files were copied
	if copiedCount == 0 {
		var message string
		if !sinceDate.IsZero() {
			message = "No new files found since " + data.Since
		} else if skippedDuplicates > 0 {
			message = "All " + strconv.Itoa(skippedDuplicates) + " files have already been imported."
		} else {
			message = "No files found to import."
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message":       message,
			"new_directory": nil,
		})
		return
	}

	// Start async thumbnail generation for imported photos
	dirName := filepath.Base(destinationDir)
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

	w.Header().Set("Content-Type", "application/json")
	var newDirectory interface{}
	if isNewBatch {
		newDirectory = dirName
	} else {
		newDirectory = nil
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":       message,
		"new_directory": newDirectory,
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

func deleteImportedHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Find USB/SD card mount point
	usbMountPoint := findUSBMountPoint()
	if usbMountPoint == "" {
		http.Error(w, "USB device with 'DCIM/100CANON' directory not found. Is it connected?", http.StatusNotFound)
		return
	}

	sourceDir := filepath.Join(usbMountPoint, "DCIM", "100CANON")
	files, err := ioutil.ReadDir(sourceDir)
	if err != nil {
		http.Error(w, "Failed to read source directory", http.StatusInternalServerError)
		return
	}

	// Build set of imported files using the same logic as the import handler
	importedFiles := buildImportedFilesSet()
	log.Printf("Delete imported: found %d already imported files", len(importedFiles))

	deletedCount := 0
	notFoundCount := 0
	errorCount := 0

	for _, file := range files {
		if !file.IsDir() && !strings.HasPrefix(file.Name(), "._") {
			lowerName := strings.ToLower(file.Name())
			// Process both .jpg and .mp4 files
			isJpg := strings.HasSuffix(lowerName, ".jpg")
			isMp4 := strings.HasSuffix(lowerName, ".mp4")
			if !isJpg && !isMp4 {
				continue
			}

			// Only delete files that are in the imported set
			if importedFiles[file.Name()] {
				filePath := filepath.Join(sourceDir, file.Name())
				if err := os.Remove(filePath); err != nil {
					if os.IsNotExist(err) {
						notFoundCount++
					} else {
						log.Printf("Failed to delete file %s: %v", filePath, err)
						errorCount++
					}
				} else {
					deletedCount++
					log.Printf("Deleted imported file: %s", file.Name())
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":      "Delete operation complete",
		"deleted":      deletedCount,
		"not_found":    notFoundCount,
		"errors":       errorCount,
		"total_found":  deletedCount + notFoundCount + errorCount,
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

func generateThumbnail(directory, filename string) error {
	thumbnailDir := filepath.Join(thumbnailCacheDir, directory)
	thumbnailPath := filepath.Join(thumbnailDir, filename)

	// Check if thumbnail already exists
	if _, err := os.Stat(thumbnailPath); err == nil {
		return nil // Already exists
	}

	originalPhotoPath := filepath.Join(photoBaseDir, directory, filename)
	file, err := os.Open(originalPhotoPath)
	if err != nil {
		return err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return err
	}

	thumb := resize.Thumbnail(uint(thumbnailSize), uint(thumbnailSize), img, resize.Lanczos3)

	if err := os.MkdirAll(thumbnailDir, 0755); err != nil {
		return err
	}

	out, err := os.Create(thumbnailPath)
	if err != nil {
		return err
	}
	defer out.Close()

	return jpeg.Encode(out, thumb, nil)
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

	// Check if thumbnail already exists
	if _, err := os.Stat(thumbnailPath); err == nil {
		http.ServeFile(w, r, thumbnailPath)
		return
	}

	// Generate thumbnail on-demand if it doesn't exist
	if err := generateThumbnail(directory, filename); err != nil {
		http.Error(w, "Failed to generate thumbnail", http.StatusInternalServerError)
		log.Printf("Error generating thumbnail for %s/%s: %v", directory, filename, err)
		return
	}

	http.ServeFile(w, r, thumbnailPath)
}
