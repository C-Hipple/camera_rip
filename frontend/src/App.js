import React, { useState, useEffect, useCallback, useRef } from 'react';
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
    const [deletedPhotos, setDeletedPhotos] = useState(new Set());
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
    const [showDeletePhotosModal, setShowDeletePhotosModal] = useState(false);
    const [isDeletingPhotos, setIsDeletingPhotos] = useState(false);
    const [carouselFilter, setCarouselFilter] = useState('all');
    const [showControls, setShowControls] = useState(false);
    const currentPhotoNameRef = useRef(null);

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
                setDeletedPhotos(new Set()); // Clear deletion marks on directory change
            })
            .catch(err => {
                setSavedPhotos(new Set()); // Default to empty set on error
                setSelectedPhotos(new Set());
                setDeletedPhotos(new Set());
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
                // Remove from deleted if it was marked for deletion
                setDeletedPhotos(prevDeleted => {
                    const newDeleted = new Set(prevDeleted);
                    newDeleted.delete(photoName);
                    return newDeleted;
                });
            } else {
                newSelected.delete(photoName);
            }
            return newSelected;
        });
    }, [savedPhotos]);

    const handleDeletion = useCallback((photoName, markForDeletion) => {
        if (savedPhotos.has(photoName)) {
            return; // Cannot mark saved photos for deletion
        }
        setDeletedPhotos(prevDeleted => {
            const newDeleted = new Set(prevDeleted);
            if (markForDeletion) {
                newDeleted.add(photoName);
                // Remove from selected if it was selected
                setSelectedPhotos(prevSelected => {
                    const newSelected = new Set(prevSelected);
                    newSelected.delete(photoName);
                    return newSelected;
                });
            } else {
                newDeleted.delete(photoName);
            }
            return newDeleted;
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

    const handleExportCurrentRaw = async () => {
        if (!currentPhotoName) return;

        const toastId = toast.loading(`Exporting raw file for ${currentPhotoName}...`);
        try {
            const response = await fetch(`${API_URL}/api/export-raw-single`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    directory: currentDirectory,
                    filename: currentPhotoName
                })
            });
            const data = await response.json();
            if (response.ok) {
                if (data.status === 'skipped') {
                    toast.update(toastId, { render: "Raw file already exported.", type: "info", isLoading: false, autoClose: 3000 });
                } else {
                    toast.update(toastId, { render: "Raw file exported successfully.", type: "success", isLoading: false, autoClose: 3000 });
                    fetchExportStatus(); // Update export status
                }
            } else {
                toast.update(toastId, { render: data.error || 'An unknown error occurred.', type: "error", isLoading: false, autoClose: 5000 });
            }
        } catch (err) {
            toast.update(toastId, { render: "Failed to export raw file.", type: "error", isLoading: false, autoClose: 5000 });
        }
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

    const handleDeletePhotos = async () => {
        setIsDeletingPhotos(true);
        setShowDeletePhotosModal(false);
        const toastId = toast.loading("Deleting photos from hard drive...");
        try {
            const filesToDelete = Array.from(deletedPhotos);
            const response = await fetch(`${API_URL}/api/delete-photos`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    directory: currentDirectory,
                    files: filesToDelete
                })
            });
            const data = await response.json();
            if (response.ok) {
                const message = `Deleted ${data.deleted} photos from hard drive${data.errors > 0 ? ` (${data.errors} errors)` : ''}`;
                toast.update(toastId, { render: message, type: "success", isLoading: false, autoClose: 5000 });
                // Refresh photos list
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
                    .catch(err => toast.error("Error refreshing photos."));
                // Clear deleted photos set
                setDeletedPhotos(new Set());
            } else {
                toast.update(toastId, { render: data.error || 'An unknown error occurred.', type: "error", isLoading: false, autoClose: 5000 });
            }
        } catch (err) {
            toast.update(toastId, { render: "Failed to delete photos.", type: "error", isLoading: false, autoClose: 5000 });
        }
        setIsDeletingPhotos(false);
    };

    // Filter photos based on carousel filter mode
    const filteredPhotos = React.useMemo(() => {
        if (carouselFilter === 'selected') {
            return photos.filter(photo => selectedPhotos.has(photo) || savedPhotos.has(photo));
        } else if (carouselFilter === 'deleted') {
            return photos.filter(photo => deletedPhotos.has(photo));
        }
        return photos;
    }, [photos, carouselFilter, selectedPhotos, savedPhotos, deletedPhotos]);

    // Track current photo name
    useEffect(() => {
        if (filteredPhotos.length > 0 && currentIndex < filteredPhotos.length) {
            currentPhotoNameRef.current = filteredPhotos[currentIndex];
        }
    }, [currentIndex, filteredPhotos]);

    // Update currentIndex when filter changes
    useEffect(() => {
        if (filteredPhotos.length === 0) {
            setCurrentIndex(0);
            return;
        }
        const currentPhotoName = currentPhotoNameRef.current;
        if (currentPhotoName && filteredPhotos.includes(currentPhotoName)) {
            // Photo still in filtered list, find its new index
            const newIndex = filteredPhotos.findIndex(photo => photo === currentPhotoName);
            if (newIndex >= 0) {
                setCurrentIndex(newIndex);
            } else {
                setCurrentIndex(0);
            }
        } else {
            // Current photo not in filtered list, go to first photo
            setCurrentIndex(0);
        }
    }, [carouselFilter]); // Only run when filter changes

    // Ensure currentIndex is valid when filteredPhotos changes (e.g., when selections change)
    useEffect(() => {
        if (filteredPhotos.length === 0) {
            setCurrentIndex(0);
            return;
        }
        const currentPhotoName = currentPhotoNameRef.current;
        if (currentPhotoName && filteredPhotos.includes(currentPhotoName)) {
            // Current photo still in filtered list, ensure index is correct
            const correctIndex = filteredPhotos.findIndex(photo => photo === currentPhotoName);
            if (correctIndex >= 0) {
                setCurrentIndex(correctIndex);
            }
        } else if (currentIndex >= filteredPhotos.length) {
            // Index out of bounds, reset to 0
            setCurrentIndex(0);
        }
    }, [filteredPhotos, currentIndex]); // Include currentIndex to check bounds

    const navigate = useCallback((direction) => {
        if (filteredPhotos.length === 0) return;
        const newIndex = (currentIndex + direction + filteredPhotos.length) % filteredPhotos.length;
        setCurrentIndex(newIndex);
    }, [currentIndex, filteredPhotos.length]);

    useEffect(() => {
        const handleKeyDown = (e) => {
            if (filteredPhotos.length === 0) return;
            const currentPhotoName = filteredPhotos[currentIndex];

            if (e.key === 's') {
                handleSelection(currentPhotoName, true);
            } else if (e.key === 'x') {
                handleSelection(currentPhotoName, false);
            } else if (e.key === 'd') {
                handleDeletion(currentPhotoName, !deletedPhotos.has(currentPhotoName));
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
    }, [currentIndex, filteredPhotos, handleSelection, handleDeletion, navigate, pinnedPhoto, deletedPhotos]);

    const currentPhotoName = filteredPhotos.length > 0 && currentIndex < filteredPhotos.length
        ? filteredPhotos[currentIndex]
        : null;
    const isSelected = currentPhotoName ? selectedPhotos.has(currentPhotoName) : false;
    const isSaved = currentPhotoName ? savedPhotos.has(currentPhotoName) : false;
    const isDeleted = currentPhotoName ? deletedPhotos.has(currentPhotoName) : false;
    const isPinnedSelected = pinnedPhoto ? selectedPhotos.has(pinnedPhoto) : false;
    const isPinnedSaved = pinnedPhoto ? savedPhotos.has(pinnedPhoto) : false;
    const isPinnedDeleted = pinnedPhoto ? deletedPhotos.has(pinnedPhoto) : false;

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
            <ConfirmModal
                isOpen={showDeletePhotosModal}
                onClose={() => setShowDeletePhotosModal(false)}
                onConfirm={handleDeletePhotos}
                title="Delete Photos from Hard Drive"
                message={`This will permanently delete ${deletedPhotos.size} photo(s) from your hard drive. This action cannot be undone. Are you sure you want to continue?`}
                confirmText="Delete"
                cancelText="Cancel"
                confirmButtonClass="delete-confirm"
            />

            <div className={`bottom-left-controls ${showControls ? 'open' : 'closed'}`}>
                <button
                    className="controls-toggle-button"
                    onClick={() => setShowControls(!showControls)}
                    title={showControls ? "Hide Controls" : "Show Controls"}
                >
                    {showControls ? '▼' : '⚙️'}
                </button>

                {showControls && (
                    <div className="controls-content">
                        <div className="sidebar-controls">
                            <button onClick={handleImport} disabled={isImporting} className="import-button">
                                {isImporting ? 'Importing...' : 'Import'}
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
                            <button
                                onClick={() => setShowDeleteModal(true)}
                                disabled={isDeleting}
                                className="delete-button"
                            >
                                {isDeleting ? 'Deleting...' : 'Delete Already Imported from SD Card'}
                            </button>
                        </div>
                    </div>
                )}
            </div>



            <main className="App-main">
                {photos.length > 0 ? (
                    <>
                        {filteredPhotos.length > 0 ? (
                            <div className="main-photo-area">
                                {pinnedPhoto ? (
                                    <div className="comparison-container">
                                        <PhotoViewer
                                            photoName={pinnedPhoto}
                                            directory={currentDirectory}
                                            isSelected={isPinnedSelected}
                                            isSaved={isPinnedSaved}
                                            isDeleted={isPinnedDeleted}
                                        >
                                            <p>{pinnedPhoto}</p>
                                            <p className={`status ${isPinnedSaved ? 'status-saved' : (isPinnedSelected ? 'status-selected' : (isPinnedDeleted ? 'status-deleted' : ''))}`}>
                                                {isPinnedSaved ? 'SAVED' : (isPinnedSelected ? 'SELECTED' : (isPinnedDeleted ? 'MARKED FOR DELETION' : 'Not Selected'))}
                                            </p>
                                            <p className="status status-pinned">PINNED</p>
                                        </PhotoViewer>
                                        <PhotoViewer
                                            photoName={currentPhotoName}
                                            directory={currentDirectory}
                                            isSelected={isSelected}
                                            isSaved={isSaved}
                                            isDeleted={isDeleted}
                                        >
                                            <p>{currentIndex + 1} / {filteredPhotos.length}</p>
                                            <p>{currentPhotoName}</p>
                                            <p className={`status ${isSaved ? 'status-saved' : (isSelected ? 'status-selected' : (isDeleted ? 'status-deleted' : ''))}`}>
                                                {isSaved ? 'SAVED' : (isSelected ? 'SELECTED' : (isDeleted ? 'MARKED FOR DELETION' : 'Not Selected'))}
                                            </p>
                                        </PhotoViewer>
                                    </div>
                                ) : (
                                    <PhotoViewer
                                        photoName={currentPhotoName}
                                        directory={currentDirectory}
                                        isSelected={isSelected}
                                        isSaved={isSaved}
                                        isDeleted={isDeleted}
                                    >
                                        <p>{currentIndex + 1} / {filteredPhotos.length}</p>
                                        <p>{currentPhotoName}</p>
                                        <p className={`status ${isSaved ? 'status-saved' : (isSelected ? 'status-selected' : (isDeleted ? 'status-deleted' : ''))}`}>
                                            {isSaved ? 'SAVED' : (isSelected ? 'SELECTED' : (isDeleted ? 'MARKED FOR DELETION' : 'Not Selected'))}
                                        </p>
                                    </PhotoViewer>)}
                            </div>
                        ) : (
                            <div className="main-photo-area">
                                <div className="empty-filter-message">
                                    <h2>
                                        {carouselFilter === 'selected' ? 'No Selected Photos' :
                                            carouselFilter === 'deleted' ? 'No Photos Marked for Deletion' :
                                                'No Photos'}
                                    </h2>
                                    <p>
                                        {carouselFilter === 'selected' ? 'Switch to "All Images" or select some photos to view them here.' :
                                            carouselFilter === 'deleted' ? 'Switch to "All Images" or mark some photos for deletion to view them here.' :
                                                'No photos available.'}
                                    </p>
                                </div>
                            </div>
                        )}
                        <div className="carousel-wrapper">
                            <div className="carousel-filter-container">
                                <select
                                    value={carouselFilter}
                                    onChange={e => setCarouselFilter(e.target.value)}
                                    className="carousel-filter-select"
                                >
                                    <option value="all">All Images</option>
                                    <option value="selected">Selected Only</option>
                                    <option value="deleted">Marked for Deletion</option>
                                </select>
                            </div>
                            {filteredPhotos.length > 0 ? (
                                <Carousel
                                    photos={filteredPhotos}
                                    currentIndex={currentIndex}
                                    setCurrentIndex={setCurrentIndex}
                                    currentDirectory={currentDirectory}
                                    selectedPhotos={selectedPhotos}
                                    savedPhotos={savedPhotos}
                                    deletedPhotos={deletedPhotos}
                                />
                            ) : (
                                <div className="carousel-container">
                                    <p className="carousel-empty-message">No photos to display</p>
                                </div>
                            )}
                        </div>
                    </>
                ) : (
                    <div className="welcome-message">
                        <h1>Photo Selector</h1>
                        <p>No photo directories found in <code>~/Pictures/photos</code>.</p>
                        <p>Connect a camera and use the Import button below to get started.</p>
                    </div>
                )}

                <div className="controls">
                    <button onClick={() => navigate(-1)} disabled={filteredPhotos.length === 0 || photos.length === 0}>Previous (← or j)</button>
                    <button
                        onClick={() => handleSelection(currentPhotoName, !isSelected)}
                        disabled={filteredPhotos.length === 0 || photos.length === 0 || isSaved || isDeleted || !currentPhotoName}
                        className={`select-toggle-button ${isSaved ? 'saved' : (isSelected ? 'selected' : '')}`}>
                        {isSaved ? 'SAVED' : (isSelected ? 'Unselect (x)' : 'Select (s)')}
                    </button>
                    <button
                        onClick={() => handleDeletion(currentPhotoName, !isDeleted)}
                        disabled={filteredPhotos.length === 0 || photos.length === 0 || isSaved || !currentPhotoName}
                        className={`delete-toggle-button ${isDeleted ? 'deleted' : ''}`}>
                        {isDeleted ? 'Unmark Delete (d)' : 'Mark Delete (d)'}
                    </button>
                    <button onClick={() => navigate(1)} disabled={filteredPhotos.length === 0 || photos.length === 0}>Next (→ or k)</button>
                    <button onClick={handleSave} disabled={selectedPhotos.size === 0} className="save-button">
                        Save {selectedPhotos.size} new selections
                    </button>
                    <div style={{ display: 'inline-flex', flexDirection: 'column', verticalAlign: 'middle', gap: '5px', margin: '0 10px' }}>
                        <button
                            onClick={handleExportRaw}
                            disabled={exportStatus.selected_count === 0 || isExportingRaw}
                            className="export-raw-button"
                            style={{ margin: 0 }}>
                            {isExportingRaw ? 'Exporting...' : `Export Raw Files (${exportStatus.missing_count} missing)`}
                        </button>
                        <button
                            onClick={handleExportCurrentRaw}
                            disabled={!currentPhotoName || isExportingRaw}
                            className="export-raw-button"
                            style={{ margin: 0, fontSize: '0.8rem', padding: '5px 10px', opacity: (!currentPhotoName || isExportingRaw) ? 0.5 : 0.8 }}>
                            Export Current Raw
                        </button>
                    </div>
                    {carouselFilter === 'deleted' && deletedPhotos.size > 0 && (
                        <button
                            onClick={() => setShowDeletePhotosModal(true)}
                            disabled={isDeletingPhotos}
                            className="delete-photos-button">
                            {isDeletingPhotos ? 'Deleting...' : `Delete ${deletedPhotos.size} Photo(s) from Hard Drive`}
                        </button>
                    )}
                </div>
                <div className="instructions">
                    <p>Use 's' to select, 'x' to unselect, 'd' to mark for deletion, and 'h' to pin/unpin. Press 'Escape' to clear pinned photo.</p>
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

function Carousel({ photos, currentIndex, setCurrentIndex, currentDirectory, selectedPhotos, savedPhotos, deletedPhotos }) {
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
                const isDeleted = deletedPhotos.has(photoName);
                return (
                    <div
                        key={i}
                        className={`carousel-thumbnail ${photoIndex === currentIndex ? 'active' : ''} ${isSaved ? 'saved' : (isDeleted ? 'deleted' : (isSelected ? 'selected' : ''))}`}
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
