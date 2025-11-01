package main

import (
	"embed"
		"fmt"
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
