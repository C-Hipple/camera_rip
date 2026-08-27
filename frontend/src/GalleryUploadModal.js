import React, { useState, useEffect } from 'react';
import './ConfirmModal.css';

// Collects the title and hashtags for a photo before it is posted to the
// gallery. The gallery's address and password live in the backend's
// environment, so nothing secret passes through here.
function GalleryUploadModal({ isOpen, onClose, onConfirm, photoName, galleryUrl, isBusy }) {
    const [title, setTitle] = useState('');
    const [hashtags, setHashtags] = useState('');

    // Start each upload from empty fields so one photo's title cannot follow
    // the next one to the gallery.
    useEffect(() => {
        if (isOpen) {
            setTitle('');
            setHashtags('');
        }
    }, [isOpen, photoName]);

    if (!isOpen) return null;

    const submit = () => {
        if (!isBusy) {
            onConfirm({ title: title.trim(), tags: hashtags.trim() });
        }
    };

    // Stop the app's review shortcuts ('s', 'x', 'd', ...) from firing while
    // the fields have focus.
    const handleKeyDown = (e) => {
        e.stopPropagation();
        if (e.key === 'Enter') {
            submit();
        } else if (e.key === 'Escape') {
            onClose();
        }
    };

    return (
        <div className="modal-overlay" onClick={isBusy ? undefined : onClose}>
            <div className="modal-content" onClick={(e) => e.stopPropagation()}>
                <h2 className="modal-title">Upload to Gallery</h2>
                <p className="modal-message">
                    {photoName}
                    {galleryUrl ? <span className="modal-hint"> → {galleryUrl}</span> : null}
                </p>

                <label className="modal-field-label" htmlFor="gallery-title">Title</label>
                <input
                    id="gallery-title"
                    type="text"
                    className="modal-input modal-input-stacked"
                    placeholder="Sunrise over the estuary"
                    value={title}
                    onChange={(e) => setTitle(e.target.value)}
                    onKeyDown={handleKeyDown}
                    disabled={isBusy}
                    autoFocus
                />

                <label className="modal-field-label" htmlFor="gallery-hashtags">Hashtags</label>
                <input
                    id="gallery-hashtags"
                    type="text"
                    className="modal-input modal-input-stacked"
                    placeholder="#film #goldenhour"
                    value={hashtags}
                    onChange={(e) => setHashtags(e.target.value)}
                    onKeyDown={handleKeyDown}
                    disabled={isBusy}
                />
                <p className="modal-hint">
                    Separate with spaces after a #, or with commas to keep multi-word tags
                    (<code>film, golden hour</code>). Both fields are optional.
                </p>

                <div className="modal-buttons">
                    <button
                        className="modal-button modal-button-cancel"
                        onClick={onClose}
                        disabled={isBusy}
                    >
                        Cancel
                    </button>
                    <button
                        className="modal-button modal-button-confirm modal-button-upload"
                        onClick={submit}
                        disabled={isBusy}
                    >
                        {isBusy ? 'Uploading...' : 'Upload'}
                    </button>
                </div>
            </div>
        </div>
    );
}

export default GalleryUploadModal;
