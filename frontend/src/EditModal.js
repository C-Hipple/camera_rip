import React, { useState, useEffect, useCallback, useRef } from 'react';
import './ConfirmModal.css';
import './EditModal.css';

const API_URL = process.env.REACT_APP_API_URL || 'http://localhost:5001';

// The preview canvas is backed at this size and scaled down by CSS on narrow
// windows; the crop overlay measures the rendered rect, so it follows along.
const PREVIEW_MAX_WIDTH = 640;
const PREVIEW_MAX_HEIGHT = 460;

// These mirror backend-go/edit.go so the preview shows what gets saved.
const DISPLAY_GAMMA = 2.2;
const MAX_BLACK_SHIFT = 0.25;
const MAX_HIGHLIGHT_KNEE = 0.5;
const MAX_SKY_PULL = 2.0;
const SKY_PIVOT = 0.1762;
const SKY_KNEE = 0.9;
const MAX_SHADOW_KNEE = 0.5;
const MAX_SHADOW_LIFT = 2.0;
const MAX_WHITE_BALANCE = 1.0;
const SKY_FEATHER = 0.25;
const DEFAULT_HORIZON = 100;

// A drag shorter than this is a click, not a crop, and clears the selection.
const MIN_CROP = 0.02;

export const EXPOSURE_RANGE = 2;

// Where the horizon starts out — at the bottom edge, so the pull covers the
// whole frame — and the highest the slider can pull it up the photo.
export const DEFAULT_HORIZON_PERCENT = DEFAULT_HORIZON;
export const HORIZON_MIN_PERCENT = 10;

const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v));
const clamp01 = (v) => clamp(v, 0, 1);

const FULL_CROP = { x: 0, y: 0, w: 1, h: 1 };

const isFullCrop = (crop) => !crop || (crop.x <= 0 && crop.y <= 0 && crop.w >= 1 && crop.h >= 1);

// smoothstep ramps from 0 at lo to 1 at hi, flat at both ends, so a gradient or
// mask built out of it has no visible seam where it starts or stops.
export const smoothstep = (lo, hi, v) => {
    if (hi <= lo) return 0;
    const t = clamp01((v - lo) / (hi - lo));
    return t * t * (3 - 2 * t);
};

// applyHighlights bends the top of the display range, leaving everything below
// the knee alone. A negative h rolls the highlights off: the open-ended range
// above the knee is squeezed into the gap left under white, so a sky an exposure
// lift pushed past 1.0 lands just below it with its gradients intact. A positive
// h is its exact inverse.
export const applyHighlights = (v, h) => {
    if (h === 0 || v <= 0) return v;
    const knee = 1 - Math.abs(h) * MAX_HIGHLIGHT_KNEE;
    if (v <= knee) return v;
    const span = 1 - knee;
    const t = (v - knee) / span;
    if (h < 0) return knee + span * (1 - Math.exp(-t));
    if (t >= 1) return 1 + span; // past white either way; the caller clamps
    return knee - span * Math.log(1 - t);
};

// applySkyPull scales linear light above a pivot down by `stops`, leaving
// everything below the pivot where it was, with the slope change eased in over
// a soft knee. Scaling above a pivot rather than weighting a multiply by
// brightness is what keeps the curve monotone at every strength: a brighter
// pixel can never come out darker than a dimmer one.
export const applySkyPull = (l, stops) => {
    if (stops <= 0) return l;
    const gain = Math.pow(2, -stops);
    const width = SKY_KNEE * SKY_PIVOT;
    const x = (l - SKY_PIVOT) / width;
    if (x <= -1) return l;
    if (x >= 1) return SKY_PIVOT + (l - SKY_PIVOT) * gain;
    return SKY_PIVOT + width * (x + (gain - 1) * (x + 1) * (x + 1) / 4);
};

// applyShadows opens up the bottom of the display range, leaving everything
// above the knee exactly where it was. It pins both ends — black stays black and
// the knee stays put — so it brightens what is dark without the milky wash a
// raised black point gives, and the sky, far above the knee, cannot move.
export const applyShadows = (v, s) => {
    if (s <= 0 || v <= 0 || v >= MAX_SHADOW_KNEE) return v;
    const t = v / MAX_SHADOW_KNEE;
    return MAX_SHADOW_KNEE * (t + s * MAX_SHADOW_LIFT * t * (1 - t) * (1 - t));
};

// whiteBalanceGains turns the temperature and tint sliders into the linear-light
// gain each colour channel takes. Temperature trades red against blue, the way
// the light's colour actually shifts; tint moves green against the other two.
export const whiteBalanceGains = (temperature, tint) => {
    const t = clamp((temperature || 0) / 100, -1, 1) * MAX_WHITE_BALANCE;
    const m = clamp((tint || 0) / 100, -1, 1) * MAX_WHITE_BALANCE;
    return { r: Math.pow(2, t), g: Math.pow(2, -m), b: Math.pow(2, -t) };
};

// greyPointWhiteBalance solves for the temperature and tint that turn one
// sampled colour neutral: pick the sky, which should be grey, and the cast goes.
// With gains of 2^t on red, 2^-m on green and 2^-t on blue, making all three
// channels equal in linear light is two equations in two unknowns.
export const greyPointWhiteBalance = (r, g, b) => {
    const lin = (c) => Math.pow(Math.max(1, c) / 255, DISPLAY_GAMMA);
    const [lr, lg, lb] = [lin(r), lin(g), lin(b)];
    const t = 0.5 * Math.log2(lb / lr);
    const m = Math.log2(lr) + t - Math.log2(lg);
    // The `|| 0` keeps a neutral sample from reporting -0, which would show up
    // on the slider's readout as "-0" and ride along into the saved edit.
    const toSlider = (stops) => Math.round(clamp(stops / MAX_WHITE_BALANCE, -1, 1) * 100) || 0;
    return { temperature: toSlider(t), tint: toSlider(-m) };
};

// skyGradient is the share of the sky pull row y of an h-row frame takes: the
// whole frame down to the horizon, then easing away over the feather band below
// it so the ground keeps the exposure it was given. With the horizon at the
// bottom edge the feather falls off the frame and every row takes the pull in
// full, which is what a bird against nothing but sky wants. The backend grades
// the whole frame before cropping, so this measures against the full photo in
// both places.
export const skyGradient = (y, height, horizon) => {
    if (height <= 0) return 0;
    const edge = clamp01(horizon / 100);
    return 1 - smoothstep(edge, edge + SKY_FEATHER, (y + 0.5) / height);
};

// skyPullStops turns the slider into stops of exposure taken off the sky. It
// only ever darkens — brightening the sky is what the exposure slider is for.
export const skyPullStops = (sky) => (sky > 0 ? (Math.min(sky, 100) / 100) * MAX_SKY_PULL : 0);

// shadowLift turns the slider into the fraction the lift curve works in. Like
// the sky pull it only goes one way: deepening the shadows is the black point's
// job, and giving one job to two sliders only makes them harder to predict.
export const shadowLift = (shadows) => (shadows > 0 ? Math.min(shadows, 100) / 100 : 0);

// buildToneLUT maps each 8-bit channel value through the white balance gain, the
// exposure shift, this row's share of the sky pull, the highlight roll-off, the
// shadow lift and the black point — exactly as the backend does. White balance,
// exposure and the sky pull work in linear light so they behave like changing
// the light or the aperture; highlights, shadows and the black point work in
// display space where they read as rolled-off highlights, open shadows and
// deeper blacks. Nothing is clamped until the end, which is what lets the
// highlight shoulder pull a blown value back under white.
//
// The fields mirror the backend's toneParams one for one.
export const buildToneLUT = ({ channelGain = 1, exposure = 0, skyPull = 0, highlights = 0, shadows = 0, black = 0 }) => {
    const gain = Math.pow(2, exposure) * channelGain;
    const h = clamp(highlights / 100, -1, 1);
    const b = clamp(black / 100, -1, 1) * MAX_BLACK_SHIFT;
    const lut = new Uint8ClampedArray(256);
    for (let i = 0; i < 256; i++) {
        let v = applySkyPull(Math.pow(i / 255, DISPLAY_GAMMA) * gain, skyPull);
        v = applyShadows(applyHighlights(Math.pow(v, 1 / DISPLAY_GAMMA), h), shadows);
        lut[i] = Math.round(clamp01((v - b) / (1 - b)) * 255);
    }
    return lut;
};

// buildRowLUTs renders the three channel curves one row of pixels goes through:
// the same curve throughout, differing only in the white balance gain.
export const buildRowLUTs = (settings, skyPull) => {
    const gains = whiteBalanceGains(settings.temperature, settings.tint);
    const base = {
        exposure: settings.exposure,
        skyPull,
        highlights: settings.highlights,
        shadows: shadowLift(settings.shadows),
        black: settings.black,
    };
    return {
        r: buildToneLUT({ ...base, channelGain: gains.r }),
        g: buildToneLUT({ ...base, channelGain: gains.g }),
        b: buildToneLUT({ ...base, channelGain: gains.b }),
    };
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
    const [temperature, setTemperature] = useState(0);
    const [tint, setTint] = useState(0);
    const [exposure, setExposure] = useState(0);
    const [black, setBlack] = useState(0);
    const [highlights, setHighlights] = useState(0);
    const [shadows, setShadows] = useState(0);
    const [sky, setSky] = useState(0);
    const [horizon, setHorizon] = useState(DEFAULT_HORIZON_PERCENT);
    const [crop, setCrop] = useState(null);
    const [image, setImage] = useState(null);
    const [loadFailed, setLoadFailed] = useState(false);
    // The lock outlives a single photo on purpose: cropping a shoot to one
    // shape only needs checking once, the way the gallery album stays picked.
    const [aspectLock, setAspectLock] = useState(false);
    const [aspectPreset, setAspectPreset] = useState('original');

    // Armed by the grey-point button; the next click on the photo samples
    // instead of starting a crop.
    const [picking, setPicking] = useState(false);

    const canvasRef = useRef(null);
    const frameRef = useRef(null);
    const dragRef = useRef(null);
    // The photo as drawn, before any of the sliders touched it. The picker has
    // to solve against the original colours, not the ones it is looking at.
    const sourceFrameRef = useRef(null);

    // Edits always render from the pristine original, so the editor previews
    // that too — otherwise the sliders would stack on the last render.
    const sourceUrl = photoName && directory
        ? `${API_URL}/photos/${encodeURIComponent(directory)}/${isEdited ? 'unedited/' : ''}${encodeURIComponent(photoName)}`
        : null;

    // Opening the editor loads the saved adjustments back into the controls.
    useEffect(() => {
        if (!isOpen) return;
        setTemperature(initialEdit?.temperature || 0);
        setTint(initialEdit?.tint || 0);
        setExposure(initialEdit?.exposure || 0);
        setBlack(initialEdit?.black || 0);
        setHighlights(initialEdit?.highlights || 0);
        setShadows(initialEdit?.shadows || 0);
        setSky(initialEdit?.sky || 0);
        setHorizon(initialEdit?.horizon || DEFAULT_HORIZON_PERCENT);
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

    // Redraw whenever the image or any of the controls change.
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
        try {
            const frame = ctx.getImageData(0, 0, width, height);
            // Keep the untouched pixels for the grey-point picker, which has to
            // solve against the photo's own colours rather than the preview's.
            sourceFrameRef.current = frame;
            if (exposure === 0 && black === 0 && highlights === 0 &&
                shadows === 0 && sky === 0 && temperature === 0 && tint === 0) {
                return;
            }

            const adjusted = ctx.createImageData(width, height);
            const src = frame.data;
            const px = adjusted.data;
            // The sky pull is the only adjustment that varies down the frame, so
            // without one a single trio of LUTs covers the lot; with one each row
            // gets its own, the way the backend walks the full-size photo. The
            // gradient is flat above the horizon and gone below the feather, so
            // only the rows inside the feather band need new ones.
            const settings = { temperature, tint, exposure, black, highlights, shadows };
            const pull = skyPullStops(sky);
            const base = buildRowLUTs(settings, 0);
            let graded = base;
            let gradedFor = -1;
            for (let y = 0; y < height; y++) {
                let luts = base;
                if (pull) {
                    const weight = skyGradient(y, height, horizon);
                    if (weight > 0) {
                        if (weight !== gradedFor) {
                            graded = buildRowLUTs(settings, pull * weight);
                            gradedFor = weight;
                        }
                        luts = graded;
                    }
                }
                const end = (y + 1) * width * 4;
                for (let i = y * width * 4; i < end; i += 4) {
                    px[i] = luts.r[src[i]];
                    px[i + 1] = luts.g[src[i + 1]];
                    px[i + 2] = luts.b[src[i + 2]];
                    px[i + 3] = src[i + 3];
                }
            }
            ctx.putImageData(adjusted, 0, 0);
        } catch (e) {
            // A tainted canvas can't be read back; the preview then shows the
            // photo unadjusted rather than nothing at all.
            sourceFrameRef.current = null;
        }
    }, [image, temperature, tint, exposure, black, highlights, shadows, sky, horizon]);

    // sampleGrey reads a small patch of the untouched photo around a point and
    // sets the white balance that turns it neutral. A patch rather than a single
    // pixel so sensor noise in a smooth sky cannot swing the answer.
    const sampleGrey = useCallback((point) => {
        const frame = sourceFrameRef.current;
        const canvas = canvasRef.current;
        if (!frame || !canvas) return false;
        const { width, height } = frame;
        const cx = Math.round(point.x * (width - 1));
        const cy = Math.round(point.y * (height - 1));

        const radius = 2;
        let r = 0, g = 0, b = 0, n = 0;
        for (let y = Math.max(0, cy - radius); y <= Math.min(height - 1, cy + radius); y++) {
            for (let x = Math.max(0, cx - radius); x <= Math.min(width - 1, cx + radius); x++) {
                const i = (y * width + x) * 4;
                r += frame.data[i];
                g += frame.data[i + 1];
                b += frame.data[i + 2];
                n++;
            }
        }
        if (!n) return false;
        const wb = greyPointWhiteBalance(r / n, g / n, b / n);
        setTemperature(wb.temperature);
        setTint(wb.tint);
        return true;
    }, []);

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
            if (event.key !== 'Escape' || isBusy) return;
            // Escape backs out of the picker first, the editor second.
            if (picking) {
                setPicking(false);
            } else {
                onClose();
            }
        };
        window.addEventListener('keydown', handleKey);
        return () => window.removeEventListener('keydown', handleKey);
    }, [isOpen, isBusy, onClose, picking]);

    const handlePointerDown = (event) => {
        if (isBusy || !image) return;
        event.preventDefault();
        const point = pointAt(event);

        // While the picker is armed a click sets the white balance instead of
        // starting a crop, then puts the pointer back to cropping.
        if (picking) {
            sampleGrey(point);
            setPicking(false);
            return;
        }

        const handle = event.target.dataset && event.target.dataset.handle;
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
        onApply({ crop: cropped ? box : null, temperature, tint, exposure, black, highlights, shadows, sky, horizon });
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
                            className={`edit-canvas-frame ${picking ? 'picking' : ''}`}
                            ref={frameRef}
                            onPointerDown={handlePointerDown}
                        >
                            <canvas ref={canvasRef} className="edit-canvas" />
                            {sky > 0 && horizon < 100 && (
                                <div
                                    className="edit-horizon-guide"
                                    style={{ top: `${horizon}%` }}
                                    aria-hidden="true"
                                >
                                    <span className="edit-horizon-label">horizon</span>
                                </div>
                            )}
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
                    <div className="edit-group-row">
                        <h3 className="edit-group-title">Colour</h3>
                        <button
                            type="button"
                            className={`modal-button modal-button-cancel edit-picker-button ${picking ? 'armed' : ''}`}
                            onClick={() => setPicking(p => !p)}
                            disabled={isBusy || !image}
                            aria-pressed={picking}
                            title="Click something in the photo that should be neutral grey — an overcast sky does nicely"
                        >
                            {picking ? 'Click a grey…' : 'Pick grey'}
                        </button>
                    </div>
                    <div className="edit-slider-row">
                        <label htmlFor="edit-temperature">Temperature</label>
                        <input
                            id="edit-temperature"
                            type="range"
                            min="-100"
                            max="100"
                            step="1"
                            value={temperature}
                            disabled={isBusy}
                            onChange={(e) => setTemperature(parseInt(e.target.value, 10))}
                        />
                        <span className="edit-slider-value">{temperature > 0 ? '+' : ''}{temperature}</span>
                    </div>
                    <div className="edit-slider-row">
                        <label htmlFor="edit-tint">Tint</label>
                        <input
                            id="edit-tint"
                            type="range"
                            min="-100"
                            max="100"
                            step="1"
                            value={tint}
                            disabled={isBusy}
                            onChange={(e) => setTint(parseInt(e.target.value, 10))}
                        />
                        <span className="edit-slider-value">{tint > 0 ? '+' : ''}{tint}</span>
                    </div>

                    <h3 className="edit-group-title">Tone</h3>
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
                    <div className="edit-slider-row">
                        <label htmlFor="edit-highlights">Highlights</label>
                        <input
                            id="edit-highlights"
                            type="range"
                            min="-100"
                            max="100"
                            step="1"
                            value={highlights}
                            disabled={isBusy}
                            onChange={(e) => setHighlights(parseInt(e.target.value, 10))}
                        />
                        <span className="edit-slider-value">{highlights > 0 ? '+' : ''}{highlights}</span>
                    </div>
                    <div className="edit-slider-row">
                        <label htmlFor="edit-shadows">Shadows</label>
                        <input
                            id="edit-shadows"
                            type="range"
                            min="0"
                            max="100"
                            step="1"
                            value={shadows}
                            disabled={isBusy}
                            onChange={(e) => setShadows(parseInt(e.target.value, 10))}
                        />
                        <span className="edit-slider-value">{shadows === 0 ? 'off' : `+${shadows}`}</span>
                    </div>
                    <div className="edit-slider-row">
                        <label htmlFor="edit-sky">Sky</label>
                        <input
                            id="edit-sky"
                            type="range"
                            min="0"
                            max="100"
                            step="1"
                            value={sky}
                            disabled={isBusy}
                            onChange={(e) => setSky(parseInt(e.target.value, 10))}
                        />
                        <span className="edit-slider-value">{sky === 0 ? 'off' : `-${skyPullStops(sky).toFixed(2)} EV`}</span>
                    </div>
                    <div className="edit-slider-row">
                        <label htmlFor="edit-horizon">Horizon</label>
                        <input
                            id="edit-horizon"
                            type="range"
                            min={HORIZON_MIN_PERCENT}
                            max="100"
                            step="1"
                            value={horizon}
                            disabled={isBusy || sky === 0}
                            onChange={(e) => setHorizon(parseInt(e.target.value, 10))}
                        />
                        <span className="edit-slider-value">{horizon >= 100 ? 'whole frame' : `${horizon}%`}</span>
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
                            onClick={() => {
                                setCrop(clearedCrop());
                                setTemperature(0);
                                setTint(0);
                                setExposure(0);
                                setBlack(0);
                                setHighlights(0);
                                setShadows(0);
                                setSky(0);
                                setHorizon(DEFAULT_HORIZON_PERCENT);
                                setPicking(false);
                            }}
                            disabled={isBusy}
                        >
                            Reset adjustments
                        </button>
                    </div>
                    <p className="modal-hint edit-hint">
                        For a bird against a bright sky, reach for <strong>Shadows</strong> before
                        Exposure: it opens the bird up and cannot touch the sky at all, where raising
                        Exposure lifts both and blows the sky white. If the sky is already gone,
                        <strong> Highlights</strong> rolls the top of the range off to stop it clipping,
                        and <strong>Sky</strong> takes up to two stops off the sky itself, leaving the
                        darker half of the frame where it was. Sky covers the whole photo until you drop
                        the <strong>Horizon</strong>, which keeps the pull off everything below the line.
                    </p>
                    <p className="modal-hint edit-hint">
                        <strong>Pick grey</strong> sets Temperature and Tint from one click: choose
                        something in the photo that ought to be neutral — an overcast sky, a pale
                        branch — and the cast comes off the whole frame.
                    </p>
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
