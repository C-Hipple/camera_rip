import React, { useState, useCallback, useRef, useEffect } from 'react';

const API_URL = process.env.REACT_APP_API_URL || 'http://localhost:5001';

const MIN_ZOOM = 0.5;
const MAX_ZOOM = 5;

function PhotoViewer({ photoName, directory, isSelected, isSaved, isDeleted, children }) {
    const [zoom, setZoom] = useState(1);
    const [position, setPosition] = useState({ x: 0, y: 0 });
    const [isPanning, setIsPanning] = useState(false);
    const [startPanPosition, setStartPanPosition] = useState({ x: 0, y: 0 });
    const containerRef = useRef(null);
    const wrapperRef = useRef(null);
    const imageRef = useRef(null);

    // Refs mirror zoom/position so native (non-React) event listeners always
    // read the current values without re-attaching on every state change
    const zoomRef = useRef(zoom);
    zoomRef.current = zoom;
    const positionRef = useRef(position);
    positionRef.current = position;

    const resetZoomAndPan = useCallback(() => {
        setZoom(1);
        setPosition({ x: 0, y: 0 });
    }, []);

    // Constrain position to keep image within container bounds
    const constrainPosition = useCallback((newPosition, currentZoom) => {
        if (!containerRef.current || !imageRef.current) return newPosition;
        if (currentZoom <= 1) return { x: 0, y: 0 };

        const container = containerRef.current;
        const image = imageRef.current;
        const containerRect = container.getBoundingClientRect();

        // Get natural image dimensions or fallback to displayed dimensions
        const imageWidth = image.naturalWidth || image.offsetWidth;
        const imageHeight = image.naturalHeight || image.offsetHeight;

        // Safety check: ensure we have valid dimensions
        if (!imageWidth || !imageHeight || imageWidth === 0 || imageHeight === 0) {
            return newPosition;
        }

        // Calculate aspect ratios
        const containerAspect = containerRect.width / containerRect.height;
        const imageAspect = imageWidth / imageHeight;

        // Calculate displayed dimensions (image fits within container maintaining aspect)
        let displayedWidth, displayedHeight;
        if (imageAspect > containerAspect) {
            // Image is wider - constrained by width
            displayedWidth = containerRect.width;
            displayedHeight = containerRect.width / imageAspect;
        } else {
            // Image is taller - constrained by height
            displayedHeight = containerRect.height;
            displayedWidth = containerRect.height * imageAspect;
        }

        // Calculate scaled dimensions
        const scaledWidth = displayedWidth * currentZoom;
        const scaledHeight = displayedHeight * currentZoom;

        // Calculate max allowed translation to keep image within bounds
        // When zoomed, the image can be panned but edges should stay within container
        const maxX = Math.max(0, (scaledWidth - containerRect.width) / 2);
        const maxY = Math.max(0, (scaledHeight - containerRect.height) / 2);

        // Constrain position
        const constrainedX = Math.max(-maxX, Math.min(maxX, newPosition.x));
        const constrainedY = Math.max(-maxY, Math.min(maxY, newPosition.y));

        return { x: constrainedX, y: constrainedY };
    }, []);

    // Update position constraints when zoom changes
    useEffect(() => {
        if (zoom > 1) {
            setPosition(prev => constrainPosition(prev, zoom));
        } else {
            setPosition({ x: 0, y: 0 });
        }
    }, [zoom, constrainPosition]);

    // Zoom to a new level, keeping the point under the cursor fixed
    const zoomAtPoint = useCallback((targetZoom, clientX, clientY) => {
        const newZoom = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, targetZoom));
        const currentZoom = zoomRef.current;
        if (newZoom === currentZoom) return;

        const wrapper = wrapperRef.current;
        if (!wrapper) {
            setZoom(newZoom);
            return;
        }

        // The image is flex-centered in the wrapper, so the untransformed image
        // center sits at the wrapper's center. A screen point m relates to an
        // image point u by m = position + zoom * u; keeping u under the cursor
        // across the zoom change gives the new translation below.
        const rect = wrapper.getBoundingClientRect();
        const mx = clientX - (rect.left + rect.width / 2);
        const my = clientY - (rect.top + rect.height / 2);
        const pos = positionRef.current;
        const scaleRatio = newZoom / currentZoom;
        const newPosition = {
            x: mx - (mx - pos.x) * scaleRatio,
            y: my - (my - pos.y) * scaleRatio
        };

        setZoom(newZoom);
        setPosition(constrainPosition(newPosition, newZoom));
    }, [constrainPosition]);

    // React attaches onWheel as a passive listener, so preventDefault() there
    // cannot stop the browser's own pinch-to-zoom (delivered as ctrl+wheel on
    // macOS trackpads) from zooming the whole page. Attach native non-passive
    // listeners instead so gestures over the photo only affect the photo.
    useEffect(() => {
        const container = containerRef.current;
        if (!container) return;

        const handleWheelNative = (e) => {
            e.preventDefault();
            const delta = e.deltaMode === 1 ? e.deltaY * 16 : e.deltaY;
            // Trackpad pinches arrive as wheel events with ctrlKey set; use a
            // higher sensitivity there so the pinch tracks finger movement
            const sensitivity = e.ctrlKey || e.metaKey ? 0.01 : 0.002;
            zoomAtPoint(zoomRef.current * Math.exp(-delta * sensitivity), e.clientX, e.clientY);
        };

        // Safari on macOS reports trackpad pinches via gesture events instead
        let gestureStartZoom = 1;
        const handleGestureStart = (e) => {
            e.preventDefault();
            gestureStartZoom = zoomRef.current;
        };
        const handleGestureChange = (e) => {
            e.preventDefault();
            zoomAtPoint(gestureStartZoom * e.scale, e.clientX, e.clientY);
        };
        const handleGestureEnd = (e) => {
            e.preventDefault();
        };

        container.addEventListener('wheel', handleWheelNative, { passive: false });
        container.addEventListener('gesturestart', handleGestureStart);
        container.addEventListener('gesturechange', handleGestureChange);
        container.addEventListener('gestureend', handleGestureEnd);
        return () => {
            container.removeEventListener('wheel', handleWheelNative);
            container.removeEventListener('gesturestart', handleGestureStart);
            container.removeEventListener('gesturechange', handleGestureChange);
            container.removeEventListener('gestureend', handleGestureEnd);
        };
        // photoName/directory re-run this after the early-return render mounts the container
    }, [zoomAtPoint, photoName, directory]);

    // --- Pan Handlers ---
    const handleMouseDown = (e) => {
        if (zoom <= 1) return;
        e.preventDefault();
        setIsPanning(true);
        setStartPanPosition({ x: e.clientX - position.x, y: e.clientY - position.y });
    };

    const handleMouseMove = (e) => {
        if (!isPanning) return;
        e.preventDefault();
        const newPosition = {
            x: e.clientX - startPanPosition.x,
            y: e.clientY - startPanPosition.y
        };
        setPosition(constrainPosition(newPosition, zoom));
    };

    const handleMouseUpOrLeave = () => {
        setIsPanning(false);
    };
    // --- End of Pan Handlers ---

    // Reset zoom when the photo changes
    React.useEffect(() => {
        resetZoomAndPan();
    }, [photoName, resetZoomAndPan]);

    if (!photoName || !directory) {
        return null;
    }

    return (
        <div className="photo-container" ref={containerRef}>
            <div className="photo-wrapper" ref={wrapperRef}>
                <img
                    ref={imageRef}
                    src={`${API_URL}/photos/${encodeURIComponent(directory)}/${encodeURIComponent(photoName)}`}
                    alt={photoName}
                    className={`photo-display ${isSaved ? 'saved' : (isDeleted ? 'deleted' : (isSelected ? 'selected' : ''))}`}
                    style={{
                        transform: `translate(${position.x}px, ${position.y}px) scale(${zoom})`,
                        transformOrigin: 'center center',
                        cursor: isPanning ? 'grabbing' : (zoom > 1 ? 'grab' : 'default')
                    }}
                    onMouseDown={handleMouseDown}
                    onMouseMove={handleMouseMove}
                    onMouseUp={handleMouseUpOrLeave}
                    onMouseLeave={handleMouseUpOrLeave}
                />
            </div>
            <div className="photo-info">
                {children}
            </div>
        </div>
    );
}

export default PhotoViewer;
