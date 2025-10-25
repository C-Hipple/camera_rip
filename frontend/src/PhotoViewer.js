import React, { useState, useCallback } from 'react';

const API_URL = process.env.REACT_APP_API_URL || 'http://localhost:5001';

function PhotoViewer({ photoName, directory, isSelected, children }) {
    const [zoom, setZoom] = useState(1);
    const [position, setPosition] = useState({ x: 0, y: 0 });
    const [isPanning, setIsPanning] = useState(false);
    const [startPanPosition, setStartPanPosition] = useState({ x: 0, y: 0 });

    const resetZoomAndPan = useCallback(() => {
        setZoom(1);
        setPosition({ x: 0, y: 0 });
    }, []);

    // --- Zoom and Pan Handlers ---
    const handleWheel = (e) => {
        e.preventDefault();
        const zoomFactor = 0.1;
        if (e.deltaY < 0) {
            setZoom(prev => Math.min(prev + zoomFactor, 5));
        } else {
            setZoom(prev => Math.max(prev - zoomFactor, 0.5));
        }
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
        setPosition({ x: e.clientX - startPanPosition.x, y: e.clientY - startPanPosition.y });
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
        <div className="photo-container">
            <img
                src={`${API_URL}/photos/${directory}/${photoName}`}
                alt={photoName}
                className={`photo-display ${isSelected ? 'selected' : ''}`}
                style={{
                    transform: `translate(${position.x}px, ${position.y}px) scale(${zoom})`,
                    cursor: isPanning ? 'grabbing' : (zoom > 1 ? 'grab' : 'default')
                }}
                onWheel={handleWheel}
                onMouseDown={handleMouseDown}
                onMouseMove={handleMouseMove}
                onMouseUp={handleMouseUpOrLeave}
                onMouseLeave={handleMouseUpOrLeave}
            />
            <div className="photo-info">
                {children}
            </div>
        </div>
    );
}

export default PhotoViewer;
