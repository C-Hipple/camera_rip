import React, { useState, useEffect, useCallback } from 'react';
import { ToastContainer, toast } from 'react-toastify';
import 'react-toastify/dist/ReactToastify.css';
import './App.css';
import PhotoViewer from './PhotoViewer';
import ConfirmModal from './ConfirmModal';

const API_URL = process.env.REACT_APP_API_URL || 'http://localhost:5001';

function App() {
    const [directories, setDirectories] = useState([]);
    const [currentDirectory, setCurrentDirectory] = useState('');
    const [photos, setPhotos] = useState([]);
    const [currentIndex, setCurrentIndex] = useState(0);
    const [selectedPhotos, setSelectedPhotos] = useState(new Set());
    const [savedPhotos, setSavedPhotos] = useState(new Set());
    const [isImporting, setIsImporting] = useState(false);
    const [sinceDate, setSinceDate] = useState('');
    const [skipDuplicates, setSkipDuplicates] = useState(true);
    const [addToCurrentBatch, setAddToCurrentBatch] = useState(false);
    const [importVideos, setImportVideos] = useState(false);
    const [pinnedPhoto, setPinnedPhoto] = useState(null);
    const [exportStatus, setExportStatus] = useState({ selected_count: 0, raw_count: 0, missing_count: 0 });
    const [isExportingRaw, setIsExportingRaw] = useState(false);
    const [showDeleteModal, setShowDeleteModal] = useState(false);
    const [isDeleting, setIsDeleting] = useState(false);

    const fetchDirectories = useCallback(() => {
        fetch(`${API_URL}/api/directories`)
            .then(res => res.json())
            .then(data => {
                if (data && !data.error) {
                    setDirectories(data);
                    if (data.length > 0 && !currentDirectory) {
                        setCurrentDirectory(data[0]);
                    }
                }
            })
            .catch(err => toast.error("Error fetching directories."));
    }, [currentDirectory]);

    const fetchExportStatus = useCallback(() => {
        if (!currentDirectory) return;
        fetch(`${API_URL}/api/export-status?directory=${currentDirectory}`)
            .then(res => res.json())
            .then(data => {
                if (data && !data.error) {
                    setExportStatus(data);
                }
            })
            .catch(err => {
                // Silently fail - directory might not have a selected folder yet
                setExportStatus({ selected_count: 0, raw_count: 0, missing_count: 0 });
            });
    }, [currentDirectory]);

    useEffect(() => {
        fetchDirectories();
    }, [fetchDirectories]);

    const handleImport = async () => {
        setIsImporting(true);
        const toastId = toast.loading("Importing from USB...")
        try {
            const response = await fetch(`${API_URL}/api/import`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({ 
                    since: sinceDate,
                    skip_duplicates: skipDuplicates,
                    target_directory: addToCurrentBatch ? currentDirectory : '',
                    import_videos: importVideos
                })
            });
            const data = await response.json();
            if (response.ok) {
                toast.update(toastId, { render: data.message, type: "success", isLoading: false, autoClose: 5000 });
                if (data.new_directory && !addToCurrentBatch) {
                    fetchDirectories();
                    setCurrentDirectory(data.new_directory);
                } else if (addToCurrentBatch) {
                    // Refresh the current directory's photos
                    window.location.reload();
                }
            } else {
                toast.update(toastId, { render: data.error || 'An unknown error occurred.', type: "error", isLoading: false, autoClose: 5000 });
            }
        } catch (err) {
            toast.update(toastId, { render: "Failed to connect to the server for import.", type: "error", isLoading: false, autoClose: 5000 });
        }
        setIsImporting(false);
    };

    useEffect(() => {
        if (!currentDirectory) return;
        setPinnedPhoto(null); // Reset pinned photo when directory changes
        fetch(`${API_URL}/api/photos?directory=${currentDirectory}`)
            .then(res => res.json())
            .then(data => {
                if (data.error) {
                    toast.error(data.error);
                    setPhotos([]);
                } else {
                    setPhotos(data);
                    setCurrentIndex(0);
                }
            })
            .catch(err => toast.error("Error fetching photos."));
        
        fetch(`${API_URL}/api/selected-photos?directory=${currentDirectory}`)
            .then(res => res.json())
            .then(data => {
                if (data.error) {
                    toast.error(data.error);
                    setSavedPhotos(new Set());
                } else {
                    setSavedPhotos(new Set(data));
                }
                setSelectedPhotos(new Set()); // Clear selection on directory change
            })
            .catch(err => {
                setSavedPhotos(new Set()); // Default to empty set on error
                setSelectedPhotos(new Set());
            });

        fetchExportStatus();
    }, [currentDirectory, fetchExportStatus]);

    const handleSelection = useCallback((photoName, select) => {
        if (savedPhotos.has(photoName)) {
            return; // Cannot change selection for saved photos
        }
        setSelectedPhotos(prevSelected => {
            const newSelected = new Set(prevSelected);
            if (select) {
                newSelected.add(photoName);
            } else {
                newSelected.delete(photoName);
            }
            return newSelected;
        });
    }, [savedPhotos]);

    const handleSave = () => {
        const toastId = toast.loading("Saving...")
        const allFilesToSave = Array.from(new Set([...selectedPhotos, ...savedPhotos]));

        fetch(`${API_URL}/api/save`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({
                directory: currentDirectory,
                selected_files: allFilesToSave,
            }),
        })
        .then(res => res.json())
        .then(data => {
            if (data.error) {
                toast.update(toastId, { render: data.error, type: "error", isLoading: false, autoClose: 5000 });
            } else {
                toast.update(toastId, { render: data.message, type: "success", isLoading: false, autoClose: 5000 });
                // Move selected to saved and clear selected
                setSavedPhotos(new Set(allFilesToSave));
                setSelectedPhotos(new Set());
                fetchExportStatus(); // Update export status after save
            }
        })
        .catch(err => {
            toast.update(toastId, { render: "An error occurred while saving.", type: "error", isLoading: false, autoClose: 5000 });
        });
    };

    const handleExportRaw = async () => {
        setIsExportingRaw(true);
        const toastId = toast.loading("Exporting raw files...");
        try {
            const response = await fetch(`${API_URL}/api/export-raw`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({ directory: currentDirectory })
            });
            const data = await response.json();
            if (response.ok) {
                const message = `Exported ${data.copied} raw files (${data.skipped} already existed, ${data.not_found} not found)`;
                toast.update(toastId, { render: message, type: "success", isLoading: false, autoClose: 5000 });
                fetchExportStatus(); // Update export status after export
            } else {
                toast.update(toastId, { render: data.error || 'An unknown error occurred.', type: "error", isLoading: false, autoClose: 5000 });
            }
        } catch (err) {
            toast.update(toastId, { render: "Failed to export raw files.", type: "error", isLoading: false, autoClose: 5000 });
        }
        setIsExportingRaw(false);
    };

    const handleDeleteImported = async () => {
        setIsDeleting(true);
        setShowDeleteModal(false);
        const toastId = toast.loading("Deleting imported images from USB...");
        try {
            const response = await fetch(`${API_URL}/api/delete-imported`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                }
            });
            const data = await response.json();
            if (response.ok) {
                const message = `Deleted ${data.deleted} imported files from USB${data.errors > 0 ? ` (${data.errors} errors)` : ''}`;
                toast.update(toastId, { render: message, type: "success", isLoading: false, autoClose: 5000 });
            } else {
                toast.update(toastId, { render: data.error || 'An unknown error occurred.', type: "error", isLoading: false, autoClose: 5000 });
            }
        } catch (err) {
            toast.update(toastId, { render: "Failed to delete imported images.", type: "error", isLoading: false, autoClose: 5000 });
        }
        setIsDeleting(false);
    };

    const navigate = useCallback((direction) => {
        if (photos.length === 0) return;
        const newIndex = (currentIndex + direction + photos.length) % photos.length;
        setCurrentIndex(newIndex);
    }, [currentIndex, photos.length]);

    useEffect(() => {
        const handleKeyDown = (e) => {
            if (photos.length === 0) return;
            const currentPhotoName = photos[currentIndex];

            if (e.key === 's') {
                handleSelection(currentPhotoName, true);
            } else if (e.key === 'x') {
                handleSelection(currentPhotoName, false);
            } else if (e.key === 'h') {
                if (pinnedPhoto === currentPhotoName) {
                    setPinnedPhoto(null); // Unpin if it's the same photo
                } else {
                    setPinnedPhoto(currentPhotoName);
                }
            } else if (e.key === 'ArrowRight' || e.key === 'k') {
                navigate(1);
            } else if (e.key === 'ArrowLeft' || e.key === 'j') {
                navigate(-1);
            } else if (e.key === 'Escape') {
                setPinnedPhoto(null);
            }
        };

        window.addEventListener('keydown', handleKeyDown);
        return () => {
            window.removeEventListener('keydown', handleKeyDown);
        };
    }, [currentIndex, photos, handleSelection, navigate, pinnedPhoto]);

    const currentPhotoName = photos[currentIndex];
    const isSelected = selectedPhotos.has(currentPhotoName);
    const isSaved = savedPhotos.has(currentPhotoName);
    const isPinnedSelected = selectedPhotos.has(pinnedPhoto);
    const isPinnedSaved = savedPhotos.has(pinnedPhoto);

    return (
        <div className="App">
            <ToastContainer position="bottom-center" autoClose={5000} hideProgressBar={false} newestOnTop={false} closeOnClick rtl={false} pauseOnFocusLoss draggable pauseOnHover theme="dark" />
            <ConfirmModal
                isOpen={showDeleteModal}
                onClose={() => setShowDeleteModal(false)}
                onConfirm={handleDeleteImported}
                title="Delete Imported Images"
                message="This will permanently delete all imported images from the USB/SD card. Only files that have been imported to your computer will be deleted. This action cannot be undone. Are you sure you want to continue?"
                confirmText="Delete"
                cancelText="Cancel"
                confirmButtonClass="delete-confirm"
            />

            <div className="bottom-left-controls">
                <div className="sidebar-controls">
                    <button onClick={handleImport} disabled={isImporting} className="import-button">
                        {isImporting ? 'Importing...' : 'Import'}
                    </button>
                    <button 
                        onClick={() => setShowDeleteModal(true)} 
                        disabled={isDeleting} 
                        className="delete-button"
                    >
                        {isDeleting ? 'Deleting...' : 'Delete Imported'}
                    </button>
                    <div className="date-picker-container">
                        <label htmlFor="since-date">Since:</label>
                        <input
                            type="date"
                            id="since-date"
                            value={sinceDate}
                            onChange={e => setSinceDate(e.target.value)}
                            className="date-picker"
                        />
                    </div>
                    <div className="checkbox-container">
                        <label>
                            <input
                                type="checkbox"
                                checked={skipDuplicates}
                                onChange={e => setSkipDuplicates(e.target.checked)}
                            />
                            <span>Skip already imported</span>
                        </label>
                    </div>
                    <div className="checkbox-container">
                        <label>
                            <input
                                type="checkbox"
                                checked={addToCurrentBatch}
                                onChange={e => setAddToCurrentBatch(e.target.checked)}
                                disabled={!currentDirectory}
                            />
                            <span>Add to current batch</span>
                        </label>
                    </div>
                    <div className="checkbox-container">
                        <label>
                            <input
                                type="checkbox"
                                checked={importVideos}
                                onChange={e => setImportVideos(e.target.checked)}
                            />
                            <span>Import videos (.MP4)</span>
                        </label>
                    </div>
                </div>

                <div className="sidebar-controls">
                    {directories.length > 0 && (
                        <select
                            value={currentDirectory}
                            onChange={e => setCurrentDirectory(e.target.value)}
                            className="directory-selector"
                        >
                            {directories.map(dir => (
                                <option key={dir} value={dir}>{dir}</option>
                            ))}
                        </select>
                    )}
                </div>
            </div>



            <main className="App-main">
                {photos.length > 0 ? (
                    <>
                        <div className="main-photo-area">
                            {pinnedPhoto ? (
                                <div className="comparison-container">
                                                                         <PhotoViewer
                                                                            photoName={pinnedPhoto}
                                                                            directory={currentDirectory}
                                                                            isSelected={isPinnedSelected}
                                                                            isSaved={isPinnedSaved}
                                                                        >
                                                                            <p>{pinnedPhoto}</p>
                                                                            <p className={`status ${isPinnedSaved ? 'status-saved' : (isPinnedSelected ? 'status-selected' : '')}`}>
                                                                                {isPinnedSaved ? 'SAVED' : (isPinnedSelected ? 'SELECTED' : 'Not Selected')}
                                                                            </p>
                                                                            <p className="status status-pinned">PINNED</p>
                                                                        </PhotoViewer>
                                                                        <PhotoViewer
                                                                            photoName={currentPhotoName}
                                                                            directory={currentDirectory}
                                                                            isSelected={isSelected}
                                                                            isSaved={isSaved}
                                                                        >
                                                                            <p>{currentIndex + 1} / {photos.length}</p>
                                                                            <p>{currentPhotoName}</p>
                                                                            <p className={`status ${isSaved ? 'status-saved' : (isSelected ? 'status-selected' : '')}`}>
                                                                                {isSaved ? 'SAVED' : (isSelected ? 'SELECTED' : 'Not Selected')}
                                                                            </p>
                                                                        </PhotoViewer>
                                                                    </div>
                                                                ) : (
                                                                    <PhotoViewer
                                                                        photoName={currentPhotoName}
                                                                        directory={currentDirectory}
                                                                        isSelected={isSelected}
                                                                        isSaved={isSaved}
                                                                    >
                                                                        <p>{currentIndex + 1} / {photos.length}</p>
                                                                        <p>{currentPhotoName}</p>
                                                                        <p className={`status ${isSaved ? 'status-saved' : (isSelected ? 'status-selected' : '')}`}>
                                                                            {isSaved ? 'SAVED' : (isSelected ? 'SELECTED' : 'Not Selected')}
                                                                        </p>
                                                                    </PhotoViewer>                            )}
                        </div>
                        <Carousel
                            photos={photos}
                            currentIndex={currentIndex}
                            setCurrentIndex={setCurrentIndex}
                            currentDirectory={currentDirectory}
                            selectedPhotos={selectedPhotos}
                            savedPhotos={savedPhotos}
                        />
                    </>
                ) : (
                    <div className="welcome-message">
                        <h1>Photo Selector</h1>
                        <p>No photo directories found in <code>~/Pictures/photos</code>.</p>
                        <p>Connect a camera and use the Import button below to get started.</p>
                    </div>
                )}

                <div className="controls">
                    <button onClick={() => navigate(-1)} disabled={photos.length === 0}>Previous (← or j)</button>
                    <button
                        onClick={() => handleSelection(currentPhotoName, !isSelected)}
                        disabled={photos.length === 0 || isSaved}
                        className={`select-toggle-button ${isSaved ? 'saved' : (isSelected ? 'selected' : '')}`}>
                        {isSaved ? 'SAVED' : (isSelected ? 'Unselect (x)' : 'Select (s)')}
                    </button>
                    <button onClick={() => navigate(1)} disabled={photos.length === 0}>Next (→ or k)</button>
                    <button onClick={handleSave} disabled={selectedPhotos.size === 0} className="save-button">
                        Save {selectedPhotos.size} new selections
                    </button>
                    <button 
                        onClick={handleExportRaw} 
                        disabled={exportStatus.selected_count === 0 || isExportingRaw} 
                        className="export-raw-button">
                        {isExportingRaw ? 'Exporting...' : `Export Raw Files (${exportStatus.missing_count} missing)`}
                    </button>
                </div>
                <div className="instructions">
                    <p>Use 's' to select, 'x' to unselect, and 'h' to pin/unpin. Press 'Escape' to clear pinned photo.</p>
                    {exportStatus.selected_count > 0 && (
                        <p className="export-status">
                            Export Status: {exportStatus.selected_count} selected JPEGs, {exportStatus.raw_count} raw files exported, {exportStatus.missing_count} missing
                        </p>
                    )}
                </div>
            </main>
        </div>
    );
}

function Carousel({ photos, currentIndex, setCurrentIndex, currentDirectory, selectedPhotos, savedPhotos }) {
    const getCarouselPhotos = () => {
        const numPhotos = photos.length;
        if (numPhotos === 0) return [];

        const indexes = [];
        for (let i = -3; i <= 3; i++) {
            let index = currentIndex + i;
            // Handle wrapping around the array
            if (index < 0) {
                index = numPhotos + index;
            } else if (index >= numPhotos) {
                index = index % numPhotos;
            }
            indexes.push(index);
        }
        return indexes;
    };

    const carouselIndexes = getCarouselPhotos();

    return (
        <div className="carousel-container">
            {carouselIndexes.map((photoIndex, i) => {
                const photoName = photos[photoIndex];
                const isSelected = selectedPhotos.has(photoName);
                const isSaved = savedPhotos.has(photoName);
                return (
                    <div
                        key={i}
                        className={`carousel-thumbnail ${photoIndex === currentIndex ? 'active' : ''} ${isSaved ? 'saved' : (isSelected ? 'selected' : '')}`}
                        onClick={() => setCurrentIndex(photoIndex)}
                    >
                        <img
                            src={`${API_URL}/thumbnail/${currentDirectory}/${photoName}`}
                            alt={`thumbnail-${photoName}`}
                        />
                    </div>
                );
            })}
        </div>
    );
}

export default App;
