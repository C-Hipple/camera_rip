# Camera Photo Import & Selector

A web-based application for importing photos from your camera's SD card, reviewing them, selecting the best shots, and exporting both JPEGs and raw CR3 files. Built with a Go backend and React frontend.

## Features

- **Import from SD Card**: Automatically detects and imports JPEG photos from Canon cameras (DCIM/100CANON)
- **Photo Review**: Navigate through imported photos with keyboard shortcuts
- **Smart Selection**: Mark photos for export with visual feedback
- **Pin & Compare**: Pin one photo to compare side-by-side with others
- **Batch Export**: Copy selected JPEGs to an export folder
- **Raw File Export**: Copy corresponding CR3 raw files directly from SD card for selected photos
- **Export Status Tracking**: Track how many raw files have been exported vs. how many are missing

## Prerequisites

Before you begin, ensure you have the following installed:
- **Go 1.22+** (older versions should work too)
- **Node.js and npm** (for building the frontend)

## Installation & Setup

### 1. Clone the Repository

```bash
git clone <repository-url>
cd camera_rip
```

### 2. Install Dependencies

```bash
make install
```

This will install both frontend (npm) and backend (Go) dependencies.

### 3. Build and Run

```bash
make build-and-run
```

This will build the frontend, copy it to the backend, compile the Go binary, and start the server.

Alternatively, you can build and run separately:

```bash
make build    # Build everything
make run      # Run the application
```

The application will be available at `http://localhost:5001`

### Quick Development Run

For quick testing without building a binary, you can also run:

```bash
make frontend  # Build frontend only
cd backend-go && go run main.go
```

### How It Works

The frontend is embedded directly into the Go binary using Go's `embed` package. When you run `make build`, the React app is compiled to static files (HTML, CSS, JS), then the `//go:embed` directive in `main.go` reads these files at compile time and includes them as bytes inside the compiled binary. This creates a single, self-contained executable that serves the full web application from memory - no external files needed!

## Workflow

### 1. Import JPEGs from SD Card

1. Connect your camera's SD card to your computer
2. Click the **Import** button in the bottom-left corner
3. Optionally, set a "Since" date to only import photos after a certain date
4. JPEGs will be copied to `~/Pictures/photos/[timestamp]/`

### 2. Review and Select Photos

1. Use the directory dropdown to select an import session
2. Navigate through photos using:
   - **Arrow keys** (← →) or **j/k** keys
   - Thumbnail carousel (click to jump to a photo)
3. Select photos you want to keep:
   - Press **`s`** to select the current photo
   - Press **`x`** to unselect
4. Use the **pin feature** to compare photos:
   - Press **`h`** to pin the current photo
   - Navigate to other photos to compare side-by-side
   - Press **`h`** again or **`Esc`** to unpin
5. Click **Save selected photos** when done
6. Selected JPEGs are copied to `~/Pictures/photos/[timestamp]/selected/`

### 3. Export Raw Files

1. After saving selected photos, the **Export Raw Files** button becomes enabled
2. Keep your SD card connected
3. Click **Export Raw Files** to copy CR3 raw files from the SD card
4. Raw files are copied to `~/Pictures/photos/[timestamp]/selected/raw/`
5. The button shows how many raw files are missing
6. Export status is displayed below the controls

## Keyboard Shortcuts

- **`←` or `j`**: Previous photo
- **`→` or `k`**: Next photo
- **`s`**: Select current photo
- **`x`**: Unselect current photo
- **`h`**: Pin/unpin current photo for comparison
- **`Esc`**: Clear pinned photo

## Directory Structure

```
~/Pictures/photos/
└── 2025-11-01_14-30-45/          # Import session
    ├── IMG_0001.JPG              # Imported JPEGs
    ├── IMG_0002.JPG
    ├── IMG_0003.JPG
    └── selected/                 # Selected photos
        ├── IMG_0001.JPG          # Selected JPEGs
        ├── IMG_0003.JPG
        └── raw/                  # Raw files
            ├── IMG_0001.CR3      # Corresponding raw files
            └── IMG_0003.CR3
```

## Development

To run the frontend in development mode:

```bash
cd frontend
npm start
```

This will start the React development server on `http://localhost:3000` with hot-reloading.

## Makefile Commands

The project includes a Makefile with convenient commands:

- **`make install`**: Install frontend and backend dependencies
- **`make build`**: Build frontend and compile Go binary
- **`make run`**: Run the compiled binary
- **`make build-and-run`**: Build everything and start the server
- **`make frontend`**: Build only the frontend
- **`make clean`**: Remove all build artifacts

## Continuous Integration

The project includes GitHub Actions CI that automatically:
- Checks Go code formatting (`gofmt`) and runs `go vet`
- Builds the Go backend
- Lints and builds the React frontend
- Runs frontend tests
- Creates a full production build and uploads the binary as an artifact

The CI runs on every push to `main`/`master` branches and on all pull requests.

## Camera Compatibility

Currently configured for Canon cameras that store photos in `DCIM/100CANON`. The app looks for:
- **JPEGs**: `.jpg` and `.jpeg` files
- **Raw files**: `.CR3` files (Canon raw format)

To support other camera brands/models, modify the `findUSBMountPoint()` function in `backend-go/main.go` to look for your camera's directory structure.
