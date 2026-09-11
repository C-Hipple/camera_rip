import React, { useState, useEffect } from 'react';
import './ConfirmModal.css';

// A stable empty list, so a modal rendered without albums does not look like a
// new list on every render.
const NO_ALBUMS = [];

// Collects the title, hashtags and album for a photo before it is posted to the
// gallery. The gallery's address and password live in the backend's
// environment, so nothing secret passes through here. The albums are the ones
// the gallery itself reported, so the dropdown can only offer real ones.
function GalleryUploadModal({ isOpen, onClose, onConfirm, photoName, galleryUrl, albums = NO_ALBUMS, isBusy }) {
    const [title, setTitle] = useState('');
    const [hashtags, setHashtags] = useState('');
    // The album is deliberately not cleared between uploads: a run of photos
    // off one shoot usually belongs in one album, and picking it once is the
    // whole point of the dropdown.
    const [album, setAlbum] = useState('');

    // Start each upload from empty fields so one photo's title cannot follow
    // the next one to the gallery.
    useEffect(() => {
        if (isOpen) {
            setTitle('');
            setHashtags('');
        }
    }, [isOpen, photoName]);

    // An album that has since been deleted in the gallery cannot be uploaded
    // into, so a remembered choice that is no longer on offer falls back to
    // none rather than being posted and rejected.
    useEffect(() => {
        setAlbum(prev => (prev && !albums.some(a => a.id === prev) ? '' : prev));
    }, [albums]);

    if (!isOpen) return null;

    const submit = () => {
        if (!isBusy) {
            onConfirm({ title: title.trim(), tags: hashtags.trim(), album });
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
                    (<code>film, golden hour</code>). Every field is optional.
                </p>

                {albums.length > 0 && (
                    <>
                        <label className="modal-field-label" htmlFor="gallery-album">Album</label>
                        <select
                            id="gallery-album"
                            className="modal-input modal-input-stacked"
                            value={album}
                            onChange={(e) => setAlbum(e.target.value)}
                            onKeyDown={handleKeyDown}
                            disabled={isBusy}
                        >
                            <option value="">No album</option>
                            {albums.map(a => (
                                <option key={a.id} value={a.id}>
                                    {a.title}{a.count ? ` (${a.count})` : ''}
                                </option>
                            ))}
                        </select>
                        <p className="modal-hint">
                            The album stays picked for the next upload, so a whole shoot only
                            needs choosing once.
                        </p>
                    </>
                )}

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
