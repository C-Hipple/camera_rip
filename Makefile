.PHONY: all clean build frontend backend run install

# Default target
all: build

# Install dependencies
install:
	@echo "Installing frontend dependencies..."
	cd frontend && npm install
	@echo "Installing Go dependencies..."
	cd backend-go && go mod download

# Build frontend
frontend:
	@echo "Building frontend..."
	cd frontend && npm run build

# Copy frontend build to backend-go directory
copy-frontend: frontend
	@echo "Copying frontend build to backend-go..."
	rm -rf backend-go/frontend
	mkdir -p backend-go/frontend
	cp -r frontend/build backend-go/frontend/build

# Build Go backend
backend: copy-frontend
	@echo "Building Go backend..."
	cd backend-go && go build -o camera-rip main.go

# Build everything
build: backend
	@echo "Build complete! Binary is at backend-go/camera-rip"

# Run the application (without rebuilding)
run:
	@echo "Starting camera-rip server..."
	cd backend-go && ./camera-rip

# Build and run the application
build-and-run: build
	@echo "Starting camera-rip server..."
	cd backend-go && ./camera-rip

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -rf frontend/build
	rm -rf backend-go/frontend
	rm -f backend-go/camera-rip
	@echo "Clean complete!"


# Run backend in dev mode (skips frontend build/copy)
dev-backend:
	@echo "Starting backend in dev mode..."
	cd backend-go && env -u GOROOT go run main.go -dev

# Run frontend in dev mode
dev-frontend:
	@echo "Starting frontend in dev mode..."
	cd frontend && npm start
