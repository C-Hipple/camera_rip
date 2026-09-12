import React, { useState, useEffect, useCallback, useRef } from 'react';
import './ConfirmModal.css';
import './EditModal.css';

const API_URL = process.env.REACT_APP_API_URL || 'http://localhost:5001';

// The preview canvas is backed at this size and scaled down by CSS on narrow
// windows; the crop overlay measures the rendered rect, so it follows along.
const PREVIEW_MAX_WIDTH = 640;
const PREVIEW_MAX_HEIGHT = 460;

// These three mirror backend-go/edit.go so the preview shows what gets saved.
const DISPLAY_GAMMA = 2.2;
const MAX_BLACK_SHIFT = 0.25;

// A drag shorter than this is a click, not a crop, and clears the selection.
const MIN_CROP = 0.02;

export const EXPOSURE_RANGE = 2;

const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v));
const clamp01 = (v) => clamp(v, 0, 1);

const FULL_CROP = { x: 0, y: 0, w: 1, h: 1 };

const isFullCrop = (crop) => !crop || (crop.x <= 0 && crop.y <= 0 && crop.w >= 1 && crop.h >= 1);

// buildToneLUT maps each 8-bit channel value through the exposure shift and
// then the black point, exactly as the backend does: exposure in linear light
// so it behaves like opening the aperture, the black point in display space
// where it reads as deeper or lifted shadows.
export const buildToneLUT = (exposure, black) => {
    const gain = Math.pow(2, exposure);
    const b = clamp(black / 100, -1, 1) * MAX_BLACK_SHIFT;
    const lut = new Uint8ClampedArray(256);
    for (let i = 0; i < 256; i++) {
        const linear = clamp01(Math.pow(i / 255, DISPLAY_GAMMA) * gain);
        const shifted = (Math.pow(linear, 1 / DISPLAY_GAMMA) - b) / (1 - b);
        lut[i] = Math.round(clamp01(shifted) * 255);
    }
    return lut;
};

// rectBetween builds a normalized crop from two corners in any order.
const rectBetween = (a, b) => ({
    x: Math.min(a.x, b.x),
    y: Math.min(a.y, b.y),
    w: Math.abs(b.x - a.x),
    h: Math.abs(b.y - a.y),
});

// The ratios the crop can be locked to, written long side first. "Original"
// keeps the photo's own shape, which is also what the uncropped frame has.
export const ASPECT_PRESETS = [
    { value: 'original', label: 'Original' },
    { value: '1:1', label: 'Square 1:1', ratio: [1, 1] },
    { value: '5:4', label: '5:4', ratio: [5, 4] },
    { value: '4:3', label: '4:3', ratio: [4, 3] },
    { value: '3:2', label: '3:2', ratio: [3, 2] },
    { value: '16:9', label: '16:9', ratio: [16, 9] },
];

// normalizedAspect converts a preset into the width-to-height ratio the crop
// overlay works in. A crop of w x h fractions covers w*imageWidth by
// h*imageHeight pixels, so a wanted pixel ratio of pw:ph means
// w/h = (pw/ph) * (imageHeight/imageWidth). Returns null when there is no
// image to measure against yet, which leaves the crop unconstrained.
export const normalizedAspect = (preset, imageWidth, imageHeight) => {
    if (!imageWidth || !imageHeight) return null;
    const found = ASPECT_PRESETS.find(p => p.value === preset);
    if (!found) return null;
    if (!found.ratio) return 1; // the photo's own shape, whatever that is
    const [long, short] = found.ratio;
    // A portrait photo takes every preset the tall way round, so "3:2" on a
    // portrait shot crops to a 2:3 upright rather than turning it sideways.
    const pixelRatio = imageHeight > imageWidth ? short / long : long / short;
    return pixelRatio * (imageHeight / imageWidth);
};

// fitCropToAspect reshapes a crop to `aspect` without ever growing it, keeping
// the centre where the user left it and nudging the result back inside frame.
export const fitCropToAspect = (box, aspect) => {
    if (!(aspect > 0)) return box;
    const w = clamp01(Math.min(box.w, box.h * aspect));
    const h = clamp01(w / aspect);
    return {
        x: clamp(box.x + (box.w - w) / 2, 0, 1 - w),
        y: clamp(box.y + (box.h - h) / 2, 0, 1 - h),
        w,
        h,
    };
};

// rectFromAnchor builds the crop for a drag from a fixed corner out to the
// pointer. With an aspect locked the box follows whichever axis the pointer
// reached furthest, then shrinks to the room left before the frame edge so a
// drag into the corner stops at the ratio rather than breaking it.
export const rectFromAnchor = (anchor, point, aspect) => {
    if (!(aspect > 0)) return rectBetween(anchor, point);
    const right = point.x >= anchor.x;
    const down = point.y >= anchor.y;
    const reach = Math.max(Math.abs(point.x - anchor.x), Math.abs(point.y - anchor.y) * aspect);
    const room = Math.min(right ? 1 - anchor.x : anchor.x, (down ? 1 - anchor.y : anchor.y) * aspect);
    const w = Math.max(0, Math.min(reach, room));
    const h = w / aspect;
    return {
        x: right ? anchor.x : anchor.x - w,
        y: down ? anchor.y : anchor.y - h,
        w,
        h,
    };
};

// fitInside scales width x height down to fit the preview box, never up.
const fitInside = (width, height) => {
    const scale = Math.min(1, PREVIEW_MAX_WIDTH / width, PREVIEW_MAX_HEIGHT / height);
    return { width: Math.max(1, Math.round(width * scale)), height: Math.max(1, Math.round(height * scale)) };
};

function EditModal({ isOpen, onClose, onApply, onRevert, photoName, directory, initialEdit, isEdited, isBusy }) {
    const [exposure, setExposure] = useState(0);
    const [black, setBlack] = useState(0);
    const [crop, setCrop] = useState(null);
    const [image, setImage] = useState(null);
    const [loadFailed, setLoadFailed] = useState(false);
    // The lock outlives a single photo on purpose: cropping a shoot to one
    // shape only needs checking once, the way the gallery album stays picked.
    const [aspectLock, setAspectLock] = useState(false);
    const [aspectPreset, setAspectPreset] = useState('original');

    const canvasRef = useRef(null);
    const frameRef = useRef(null);
    const dragRef = useRef(null);

    // Edits always render from the pristine original, so the editor previews
    // that too — otherwise the sliders would stack on the last render.
    const sourceUrl = photoName && directory
        ? `${API_URL}/photos/${encodeURIComponent(directory)}/${isEdited ? 'unedited/' : ''}${encodeURIComponent(photoName)}`
        : null;

    // Opening the editor loads the saved adjustments back into the controls.
    useEffect(() => {
        if (!isOpen) return;
        setExposure(initialEdit?.exposure || 0);
        setBlack(initialEdit?.black || 0);
        setCrop(initialEdit?.crop || null);
        setImage(null);
        setLoadFailed(false);
    }, [isOpen, photoName, initialEdit]);

    useEffect(() => {
        if (!isOpen || !sourceUrl) return undefined;
        let cancelled = false;
        const el = new Image();
        // The dev server runs on a different origin than the API; without this
        // the canvas is tainted and the preview can't be read back for tone.
        el.crossOrigin = 'anonymous';
        el.onload = () => { if (!cancelled) setImage(el); };
        el.onerror = () => { if (!cancelled) setLoadFailed(true); };
        el.src = sourceUrl;
        return () => { cancelled = true; };
    }, [isOpen, sourceUrl]);

    // Redraw whenever the image or the tone controls change.
    useEffect(() => {
        const canvas = canvasRef.current;
        if (!canvas || !image) return;
        const { width, height } = fitInside(image.naturalWidth || 1, image.naturalHeight || 1);
        canvas.width = width;
        canvas.height = height;

        let ctx = null;
        try {
            ctx = canvas.getContext('2d');
        } catch (e) {
            return; // jsdom and other canvas-less environments
        }
        if (!ctx) return;

        ctx.drawImage(image, 0, 0, width, height);
        if (exposure === 0 && black === 0) return;
        try {
            const frame = ctx.getImageData(0, 0, width, height);
            const lut = buildToneLUT(exposure, black);
            const px = frame.data;
            for (let i = 0; i < px.length; i += 4) {
                px[i] = lut[px[i]];
                px[i + 1] = lut[px[i + 1]];
                px[i + 2] = lut[px[i + 2]];
            }
            ctx.putImageData(frame, 0, 0);
        } catch (e) {
            // A tainted canvas can't be read back; the preview then shows the
            // photo unadjusted rather than nothing at all.
        }
    }, [image, exposure, black]);

    // Null whenever the lock is off or the photo hasn't loaded, which is the
    // signal every crop helper below reads as "leave the shape alone".
    const aspect = aspectLock
        ? normalizedAspect(aspectPreset, image?.naturalWidth, image?.naturalHeight)
        : null;

    // Clearing a crop goes back to the full frame, or to the largest box the
    // locked ratio allows so the overlay never contradicts the lock.
    const clearedCrop = useCallback(() => (aspect ? fitCropToAspect(FULL_CROP, aspect) : null), [aspect]);

    const pointAt = useCallback((event) => {
        const frame = frameRef.current;
        if (!frame) return { x: 0, y: 0 };
        const rect = frame.getBoundingClientRect();
        if (!rect.width || !rect.height) return { x: 0, y: 0 };
        return {
            x: clamp01((event.clientX - rect.left) / rect.width),
            y: clamp01((event.clientY - rect.top) / rect.height),
        };
    }, []);

    // Dragging continues outside the frame, so the move/up listeners live on
    // the window rather than the crop box.
    useEffect(() => {
        if (!isOpen) return undefined;
        const handleMove = (event) => {
            const drag = dragRef.current;
            if (!drag) return;
            const point = pointAt(event);
            if (drag.mode === 'move') {
                setCrop({
                    x: clamp(drag.origin.x + point.x - drag.start.x, 0, 1 - drag.size.w),
                    y: clamp(drag.origin.y + point.y - drag.start.y, 0, 1 - drag.size.h),
                    w: drag.size.w,
                    h: drag.size.h,
                });
            } else {
                setCrop(rectFromAnchor(drag.anchor, point, aspect));
            }
        };
        const handleUp = () => {
            if (!dragRef.current) return;
            dragRef.current = null;
            // A click rather than a drag means "no crop" — back to the full frame.
            setCrop(current => (current && (current.w < MIN_CROP || current.h < MIN_CROP) ? clearedCrop() : current));
        };
        window.addEventListener('pointermove', handleMove);
        window.addEventListener('pointerup', handleUp);
        return () => {
            window.removeEventListener('pointermove', handleMove);
            window.removeEventListener('pointerup', handleUp);
        };
    }, [isOpen, pointAt, aspect, clearedCrop]);

    useEffect(() => {
        if (!isOpen) return undefined;
        const handleKey = (event) => {
            if (event.key === 'Escape' && !isBusy) {
                onClose();
            }
        };
        window.addEventListener('keydown', handleKey);
        return () => window.removeEventListener('keydown', handleKey);
    }, [isOpen, isBusy, onClose]);

    const handlePointerDown = (event) => {
        if (isBusy || !image) return;
        event.preventDefault();
        const handle = event.target.dataset && event.target.dataset.handle;
        const point = pointAt(event);
        const current = crop || FULL_CROP;

        if (handle) {
            // Resizing pivots on the corner opposite the one being dragged.
            dragRef.current = {
                mode: 'resize',
                anchor: {
                    x: handle.includes('w') ? current.x + current.w : current.x,
                    y: handle.includes('n') ? current.y + current.h : current.y,
                },
            };
        } else if (event.target.dataset && event.target.dataset.cropBox && !isFullCrop(crop)) {
            dragRef.current = {
                mode: 'move',
                start: point,
                origin: { x: current.x, y: current.y },
                size: { w: current.w, h: current.h },
            };
        } else {
            dragRef.current = { mode: 'resize', anchor: point };
            setCrop({ x: point.x, y: point.y, w: 0, h: 0 });
        }
    };

    // Locking, or picking a different ratio, reshapes the crop right away so
    // the box on screen always shows the shape every later drag will keep.
    const applyAspect = (locked, preset) => {
        setAspectLock(locked);
        setAspectPreset(preset);
        if (!locked) return;
        const next = normalizedAspect(preset, image?.naturalWidth, image?.naturalHeight);
        if (next) setCrop(current => fitCropToAspect(current || FULL_CROP, next));
    };

    if (!isOpen || !photoName) return null;

    const box = crop || FULL_CROP;
    const cropped = !isFullCrop(crop);
    const cropLabel = image
        ? `${Math.max(1, Math.round(box.w * image.naturalWidth))} × ${Math.max(1, Math.round(box.h * image.naturalHeight))} px`
        : '—';

    const apply = () => {
        if (isBusy) return;
        onApply({ crop: cropped ? box : null, exposure, black });
    };

    return (
        <div className="modal-overlay" onClick={() => { if (!isBusy) onClose(); }}>
            <div className="modal-content edit-modal" onClick={(e) => e.stopPropagation()}>
                <h2 className="modal-title edit-modal-title">Edit {photoName}</h2>

                <div className="edit-preview">
                    {loadFailed ? (
                        <p className="edit-preview-message">Could not load this photo for editing.</p>
                    ) : (
                        <div
                            className="edit-canvas-frame"
                            ref={frameRef}
                            onPointerDown={handlePointerDown}
                        >
                            <canvas ref={canvasRef} className="edit-canvas" />
                            <div
                                className={`edit-crop-box ${cropped ? '' : 'full-frame'}`}
                                data-crop-box="true"
                                style={{
                                    left: `${box.x * 100}%`,
                                    top: `${box.y * 100}%`,
                                    width: `${box.w * 100}%`,
                                    height: `${box.h * 100}%`,
                                }}
                            >
                                <span className="edit-crop-handle nw" data-handle="nw" />
                                <span className="edit-crop-handle ne" data-handle="ne" />
                                <span className="edit-crop-handle sw" data-handle="sw" />
                                <span className="edit-crop-handle se" data-handle="se" />
                            </div>
                            {!image && !loadFailed && <p className="edit-preview-message">Loading preview…</p>}
                        </div>
                    )}
                </div>

                <div className="edit-controls">
                    <div className="edit-slider-row">
                        <label htmlFor="edit-exposure">Exposure</label>
                        <input
                            id="edit-exposure"
                            type="range"
                            min={-EXPOSURE_RANGE}
                            max={EXPOSURE_RANGE}
                            step="0.05"
                            value={exposure}
                            disabled={isBusy}
                            onChange={(e) => setExposure(parseFloat(e.target.value))}
                        />
                        <span className="edit-slider-value">{exposure > 0 ? '+' : ''}{exposure.toFixed(2)} EV</span>
                    </div>
                    <div className="edit-slider-row">
                        <label htmlFor="edit-black">Black level</label>
                        <input
                            id="edit-black"
                            type="range"
                            min="-100"
                            max="100"
                            step="1"
                            value={black}
                            disabled={isBusy}
                            onChange={(e) => setBlack(parseInt(e.target.value, 10))}
                        />
                        <span className="edit-slider-value">{black > 0 ? '+' : ''}{black}</span>
                    </div>
                    <div className="edit-crop-row">
                        <span className="edit-crop-summary">
                            Crop: {cropped ? cropLabel : 'full frame'}
                        </span>
                        <div className="edit-aspect-controls">
                            <label className="edit-aspect-lock" htmlFor="edit-aspect-lock">
                                <input
                                    id="edit-aspect-lock"
                                    type="checkbox"
                                    checked={aspectLock}
                                    disabled={isBusy || !image}
                                    onChange={(e) => applyAspect(e.target.checked, aspectPreset)}
                                />
                                Lock ratio
                            </label>
                            <select
                                className="modal-input edit-aspect-select"
                                aria-label="Crop aspect ratio"
                                value={aspectPreset}
                                disabled={isBusy || !image || !aspectLock}
                                onChange={(e) => applyAspect(true, e.target.value)}
                            >
                                {ASPECT_PRESETS.map(preset => (
                                    <option key={preset.value} value={preset.value}>{preset.label}</option>
                                ))}
                            </select>
                        </div>
                        <button
                            type="button"
                            className="modal-button modal-button-cancel edit-reset-button"
                            onClick={() => { setCrop(clearedCrop()); setExposure(0); setBlack(0); }}
                            disabled={isBusy}
                        >
                            Reset adjustments
                        </button>
                    </div>
                    <p className="modal-hint edit-hint">
                        Drag on the photo to draw a crop, drag inside it to move, or drag a corner to resize.
                        Click once outside the box to clear it. Lock the ratio to hold every drag at the
                        same shape — presets follow the photo, so 3:2 stays upright on a portrait shot.
                    </p>
                </div>

                <div className="modal-buttons edit-modal-buttons">
                    {isEdited && (
                        <button
                            className="modal-button modal-button-confirm edit-revert-button"
                            onClick={() => { if (!isBusy) onRevert(); }}
                            disabled={isBusy}
                        >
                            Revert to Original
                        </button>
                    )}
                    <button className="modal-button modal-button-cancel" onClick={onClose} disabled={isBusy}>
                        Cancel
                    </button>
                    <button className="modal-button modal-button-upload" onClick={apply} disabled={isBusy}>
                        {isBusy ? 'Applying...' : 'Apply Edit'}
                    </button>
                </div>
            </div>
        </div>
    );
}

export default EditModal;
