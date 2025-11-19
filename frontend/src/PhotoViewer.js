import React, { useState, useCallback, useRef, useEffect } from 'react';

const API_URL = process.env.REACT_APP_API_URL || 'http://localhost:5001';

function PhotoViewer({ photoName, directory, isSelected, isSaved, isDeleted, children }) {
    const [zoom, setZoom] = useState(1);
    const [position, setPosition] = useState({ x: 0, y: 0 });
    const [isPanning, setIsPanning] = useState(false);
    const [startPanPosition, setStartPanPosition] = useState({ x: 0, y: 0 });
    const containerRef = useRef(null);
    const imageRef = useRef(null);

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

    // --- Zoom and Pan Handlers ---
    const handleWheel = (e) => {
        e.preventDefault();
        const zoomFactor = 0.1;
        const newZoom = e.deltaY < 0 
            ? Math.min(zoom + zoomFactor, 5)
            : Math.max(zoom - zoomFactor, 0.5);
        
        setZoom(newZoom);
    };

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
    // --- End of Zoom and Pan Handlers ---

    // Reset zoom when the photo changes
    React.useEffect(() => {
        resetZoomAndPan();
    }, [photoName, resetZoomAndPan]);

    if (!photoName || !directory) {
        return null;
    }

    return (
        <div className="photo-container" ref={containerRef}>
            <div className="photo-wrapper">
                <img
                    ref={imageRef}
                    src={`${API_URL}/photos/${directory}/${photoName}`}
                    alt={photoName}
                    className={`photo-display ${isSaved ? 'saved' : (isDeleted ? 'deleted' : (isSelected ? 'selected' : ''))}`}
                    style={{
                        transform: `translate(${position.x}px, ${position.y}px) scale(${zoom})`,
                        transformOrigin: 'center center',
                        cursor: isPanning ? 'grabbing' : (zoom > 1 ? 'grab' : 'default')
                    }}
                    onWheel={handleWheel}
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
