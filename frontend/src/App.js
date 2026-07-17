import React, { useState, useEffect, useCallback, useRef } from 'react';
import { ToastContainer, toast } from 'react-toastify';
import 'react-toastify/dist/ReactToastify.css';
import './App.css';
import PhotoViewer from './PhotoViewer';
import ConfirmModal from './ConfirmModal';
import RenameModal from './RenameModal';

const API_URL = process.env.REACT_APP_API_URL || 'http://localhost:5001';

// Matches the backend's default new-import folder name (2006-01-02_15-04-05).
const formatFolderTimestamp = (d) => {
    const pad = n => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}_${pad(d.getHours())}-${pad(d.getMinutes())}-${pad(d.getSeconds())}`;
};

function App() {
    const [directories, setDirectories] = useState([]);
    const [currentDirectory, setCurrentDirectory] = useState('');
    const [photos, setPhotos] = useState([]);
    const [currentIndex, setCurrentIndex] = useState(0);
    const [selectedPhotos, setSelectedPhotos] = useState(new Set());
    const [savedPhotos, setSavedPhotos] = useState(new Set());
    const [deletedPhotos, setDeletedPhotos] = useState(new Set());
    const [isImporting, setIsImporting] = useState(false);
    const [importProgress, setImportProgress] = useState(null);
    const [sinceDate, setSinceDate] = useState('');
    const [untilDate, setUntilDate] = useState('');
    const [skipDuplicates, setSkipDuplicates] = useState(true);
    const [addToCurrentBatch, setAddToCurrentBatch] = useState(false);
    const [importVideos, setImportVideos] = useState(false);
    const [importRaws, setImportRaws] = useState(false);
    const [pinnedPhoto, setPinnedPhoto] = useState(null);
    const [exportStatus, setExportStatus] = useState({ selected_count: 0, raw_count: 0, missing_count: 0 });
    const [isExportingRaw, setIsExportingRaw] = useState(false);
    const [showDeleteModal, setShowDeleteModal] = useState(false);
    const [isDeleting, setIsDeleting] = useState(false);
    const [showDeletePhotosModal, setShowDeletePhotosModal] = useState(false);
    const [isDeletingPhotos, setIsDeletingPhotos] = useState(false);
    const [carouselFilter, setCarouselFilter] = useState('all');
    const [isSidebarCollapsed, setIsSidebarCollapsed] = useState(false);
    const [showThumbnailView, setShowThumbnailView] = useState(false);
    const [isFullscreen, setIsFullscreen] = useState(false);
    const currentPhotoNameRef = useRef(null);
    const [importPreview, setImportPreview] = useState(null);
    const [isLoadingPreview, setIsLoadingPreview] = useState(false);
    const [showRenameModal, setShowRenameModal] = useState(false);
    const [isRenaming, setIsRenaming] = useState(false);
    const [photoMetadata, setPhotoMetadata] = useState(null);
    const [newFolderName, setNewFolderName] = useState(() => formatFolderTimestamp(new Date()));
    const [folderNameEdited, setFolderNameEdited] = useState(false);

    // Keep the destination folder prefill ticking with the current time until
    // the user touches it, so an untouched field always matches the moment
    // Import is clicked.
    useEffect(() => {
        if (folderNameEdited) return;
        const id = setInterval(() => setNewFolderName(formatFolderTimestamp(new Date())), 1000);
        return () => clearInterval(id);
    }, [folderNameEdited]);

    // Switch the visible directory, clearing the previous directory's photo
    // list in the same update. Clearing in an effect is too late: React
    // commits one frame pairing the new directory with the stale filenames,
    // and the carousel/viewer request photos and thumbnails that don't exist
    // under the new directory (logged as ENOENT errors by the backend).
    const switchDirectory = useCallback((dir) => {
        setPhotos([]);
        setCurrentIndex(0);
        setCurrentDirectory(dir);
    }, []);

    const fetchDirectories = useCallback(() => {
        fetch(`${API_URL}/api/directories`)
            .then(res => res.json())
            .then(data => {
                if (data && !data.error) {
                    setDirectories(data);
                    if (data.length > 0 && !currentDirectory) {
                        switchDirectory(data[0]);
                    }
                }
            })
            .catch(err => toast.error("Error fetching directories."));
    }, [currentDirectory, switchDirectory]);

    const fetchExportStatus = useCallback(() => {
        if (!currentDirectory) return;
        fetch(`${API_URL}/api/export-status?directory=${encodeURIComponent(currentDirectory)}`)
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

    const fetchImportPreview = useCallback(async () => {
        setIsLoadingPreview(true);
        try {
            const response = await fetch(`${API_URL}/api/import-preview`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    since: sinceDate,
                    until: untilDate,
                    skip_duplicates: skipDuplicates,
                    target_directory: addToCurrentBatch ? currentDirectory : '',
                    import_videos: importVideos,
                    import_raws: importRaws
                })
            });
            const data = await response.json();
            if (response.ok) {
                setImportPreview(data);
            } else {
                setImportPreview(null);
            }
        } catch (err) {
            setImportPreview(null);
        }
        setIsLoadingPreview(false);
    }, [sinceDate, untilDate, skipDuplicates, addToCurrentBatch, currentDirectory, importVideos, importRaws]);

    useEffect(() => {
        fetchImportPreview();
    }, [fetchImportPreview]);

    const handleImport = async () => {
        setIsImporting(true);
        setImportProgress(null);
        const toastId = toast.loading("Importing from USB...")
        try {
            const response = await fetch(`${API_URL}/api/import`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    since: sinceDate,
                    until: untilDate,
                    skip_duplicates: skipDuplicates,
                    target_directory: addToCurrentBatch ? currentDirectory : '',
                    new_directory_name: addToCurrentBatch ? '' : newFolderName.trim(),
                    import_videos: importVideos,
                    import_raws: importRaws
                })
            });

            // Hard failures (no USB, bad request) return non-200 plain text.
            if (!response.ok) {
                const text = await response.text();
                toast.update(toastId, { render: text.trim() || 'An unknown error occurred.', type: "error", isLoading: false, autoClose: 5000 });
                setIsImporting(false);
                return;
            }

            // Success streams newline-delimited JSON progress events.
            const reader = response.body.getReader();
            const decoder = new TextDecoder();
            let buffer = '';
            let doneEvent = null;
            let errorEvent = null;

            const handleEvent = (evt) => {
                if (evt.type === 'start') {
                    setImportProgress({ copied: 0, total: evt.total });
                    toast.update(toastId, { render: `Importing 0 / ${evt.total}...`, isLoading: true });
                } else if (evt.type === 'progress') {
                    setImportProgress({ copied: evt.copied, total: evt.total });
                    toast.update(toastId, { render: `Importing ${evt.copied} / ${evt.total}...`, isLoading: true });
                } else if (evt.type === 'done') {
                    doneEvent = evt;
                } else if (evt.type === 'error') {
                    errorEvent = evt;
                }
            };

            for (; ;) {
                const { done, value } = await reader.read();
                if (done) break;
                buffer += decoder.decode(value, { stream: true });
                let newlineIndex;
                while ((newlineIndex = buffer.indexOf('\n')) >= 0) {
                    const line = buffer.slice(0, newlineIndex).trim();
                    buffer = buffer.slice(newlineIndex + 1);
                    if (!line) continue;
                    try {
                        handleEvent(JSON.parse(line));
                    } catch (e) {
                        // Ignore malformed lines
                    }
                }
            }
            // Handle any trailing buffered line without a newline.
            const tail = buffer.trim();
            if (tail) {
                try {
                    handleEvent(JSON.parse(tail));
                } catch (e) { /* ignore */ }
            }

            if (errorEvent) {
                toast.update(toastId, { render: errorEvent.message || 'Import failed.', type: "error", isLoading: false, autoClose: 5000 });
            } else if (doneEvent) {
                toast.update(toastId, { render: doneEvent.message, type: "success", isLoading: false, autoClose: 5000 });
                if (doneEvent.new_directory && !addToCurrentBatch) {
                    fetchDirectories();
                    switchDirectory(doneEvent.new_directory);
                    // Re-arm the prefill for the next import.
                    setNewFolderName(formatFolderTimestamp(new Date()));
                    setFolderNameEdited(false);
                } else if (addToCurrentBatch) {
                    // Refresh the current directory's photos
                    window.location.reload();
                }
            } else {
                toast.update(toastId, { render: "Import finished.", type: "success", isLoading: false, autoClose: 5000 });
            }
        } catch (err) {
            toast.update(toastId, { render: "Failed to connect to the server for import.", type: "error", isLoading: false, autoClose: 5000 });
        }
        setImportProgress(null);
        setIsImporting(false);
    };

    useEffect(() => {
        if (!currentDirectory) return;
        setPinnedPhoto(null); // Reset pinned photo when directory changes
        fetch(`${API_URL}/api/photos?directory=${encodeURIComponent(currentDirectory)}`)
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

        fetch(`${API_URL}/api/selected-photos?directory=${encodeURIComponent(currentDirectory)}`)
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
                const message = `Deleted ${data.deleted} imported files${data.deleted_raw ? ` and ${data.deleted_raw} RAW files` : ''} from USB${data.errors > 0 ? ` (${data.errors} errors)` : ''}`;
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
                fetch(`${API_URL}/api/photos?directory=${encodeURIComponent(currentDirectory)}`)
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

    const handleRenameDirectory = async (newName) => {
        newName = newName.trim();
        if (!newName || newName === currentDirectory) {
            setShowRenameModal(false);
            return;
        }
        setIsRenaming(true);
        const toastId = toast.loading("Renaming folder...");
        try {
            const response = await fetch(`${API_URL}/api/rename-directory`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    directory: currentDirectory,
                    new_name: newName
                })
            });
            if (response.ok) {
                const data = await response.json();
                toast.update(toastId, { render: data.message, type: "success", isLoading: false, autoClose: 5000 });
                setShowRenameModal(false);
                // Swap the name in place so the selector stays consistent, then
                // re-fetch the list to restore server-side ordering.
                setDirectories(prev => prev.map(dir => (dir === currentDirectory ? data.new_directory : dir)));
                switchDirectory(data.new_directory);
                fetchDirectories();
            } else {
                const text = await response.text();
                toast.update(toastId, { render: text.trim() || 'Failed to rename folder.', type: "error", isLoading: false, autoClose: 5000 });
            }
        } catch (err) {
            toast.update(toastId, { render: "Failed to rename folder.", type: "error", isLoading: false, autoClose: 5000 });
        }
        setIsRenaming(false);
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

    // Calculate counts for each filter option
    const filterCounts = React.useMemo(() => {
        const selectedCount = photos.filter(photo => selectedPhotos.has(photo) || savedPhotos.has(photo)).length;
        const deletedCount = photos.filter(photo => deletedPhotos.has(photo)).length;
        return {
            all: photos.length,
            selected: selectedCount,
            deleted: deletedCount
        };
    }, [photos, selectedPhotos, savedPhotos, deletedPhotos]);

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
    }, [carouselFilter, filteredPhotos]); // Run when filter or filtered photos change

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
            // Ignore shortcuts while typing in a form field (e.g. the rename input)
            const tag = e.target.tagName;
            if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
            if (filteredPhotos.length === 0) return;
            const currentPhotoName = filteredPhotos[currentIndex];

            if (e.key === 's') {
                handleSelection(currentPhotoName, true);
            } else if (e.key === 'x') {
                handleSelection(currentPhotoName, false);
            } else if (e.key === 'd') {
                handleDeletion(currentPhotoName, !deletedPhotos.has(currentPhotoName));
            } else if (e.key === 'h') {
                if (isFullscreen) return; // Pin-to-compare disabled in fullscreen
                if (pinnedPhoto === currentPhotoName) {
                    setPinnedPhoto(null); // Unpin if it's the same photo
                } else {
                    setPinnedPhoto(currentPhotoName);
                }
            } else if (e.key === 'f') {
                setIsFullscreen(prev => {
                    const next = !prev;
                    if (next) setPinnedPhoto(null);
                    return next;
                });
            } else if (e.key === 'ArrowRight' || e.key === 'k') {
                navigate(1);
            } else if (e.key === 'ArrowLeft' || e.key === 'j') {
                navigate(-1);
            } else if (e.key === 'Escape') {
                if (isFullscreen) {
                    setIsFullscreen(false);
                } else {
                    setPinnedPhoto(null);
                }
            }
        };

        window.addEventListener('keydown', handleKeyDown);
        return () => {
            window.removeEventListener('keydown', handleKeyDown);
        };
    }, [currentIndex, filteredPhotos, handleSelection, handleDeletion, navigate, pinnedPhoto, deletedPhotos, isFullscreen]);

    const currentPhotoName = filteredPhotos.length > 0 && currentIndex < filteredPhotos.length
        ? filteredPhotos[currentIndex]
        : null;
    const isSelected = currentPhotoName ? selectedPhotos.has(currentPhotoName) : false;
    const isSaved = currentPhotoName ? savedPhotos.has(currentPhotoName) : false;
    const isDeleted = currentPhotoName ? deletedPhotos.has(currentPhotoName) : false;
    const isPinnedSelected = pinnedPhoto ? selectedPhotos.has(pinnedPhoto) : false;
    const isPinnedSaved = pinnedPhoto ? savedPhotos.has(pinnedPhoto) : false;
    const isPinnedDeleted = pinnedPhoto ? deletedPhotos.has(pinnedPhoto) : false;

    // Fetch EXIF camera settings (shutter speed, aperture, ISO, focal length)
    // for the photo on display. The cancelled flag drops stale responses when
    // navigating quickly between photos.
    useEffect(() => {
        if (!currentPhotoName || !currentDirectory) {
            setPhotoMetadata(null);
            return;
        }
        let cancelled = false;
        setPhotoMetadata(null);
        fetch(`${API_URL}/api/photo-metadata?directory=${encodeURIComponent(currentDirectory)}&photo=${encodeURIComponent(currentPhotoName)}`)
            .then(res => res.json())
            .then(data => {
                if (!cancelled) {
                    setPhotoMetadata(data && !data.error ? data : null);
                }
            })
            .catch(() => {
                if (!cancelled) {
                    setPhotoMetadata(null);
                }
            });
        return () => { cancelled = true; };
    }, [currentPhotoName, currentDirectory]);

    const metadataParts = photoMetadata
        ? [photoMetadata.shutter_speed, photoMetadata.aperture, photoMetadata.iso, photoMetadata.focal_length].filter(Boolean)
        : [];

    return (
        <div className={`App ${isFullscreen ? 'fullscreen-mode' : ''}`}>
            <ToastContainer position="bottom-center" autoClose={5000} hideProgressBar={false} newestOnTop={false} closeOnClick rtl={false} pauseOnFocusLoss draggable pauseOnHover theme="dark" />
            {isFullscreen && currentPhotoName && (
                <div className="fullscreen-overlay">
                    <div className="fullscreen-photo">
                        <PhotoViewer
                            photoName={currentPhotoName}
                            directory={currentDirectory}
                            isSelected={isSelected}
                            isSaved={isSaved}
                            isDeleted={isDeleted}
                        />
                    </div>
                    <div className="fullscreen-info">
                        <div className="fullscreen-filename">{currentPhotoName}</div>
                        <div className="fullscreen-position">{currentIndex + 1} / {filteredPhotos.length}</div>
                        <div className={`status ${isSaved ? 'status-saved' : (isSelected ? 'status-selected' : (isDeleted ? 'status-deleted' : ''))}`}>
                            {isSaved ? 'SAVED' : (isSelected ? 'SELECTED' : (isDeleted ? 'MARKED FOR DELETION' : 'Not Selected'))}
                        </div>
                    </div>
                    <div className="fullscreen-controls">
                        <button onClick={() => navigate(-1)}>← (j)</button>
                        <button
                            onClick={() => handleSelection(currentPhotoName, !isSelected)}
                            disabled={isSaved || isDeleted}
                            className={`select-toggle-button ${isSaved ? 'saved' : (isSelected ? 'selected' : '')}`}>
                            {isSaved ? 'SAVED' : (isSelected ? 'Unselect (x)' : 'Select (s)')}
                        </button>
                        <button
                            onClick={() => handleDeletion(currentPhotoName, !isDeleted)}
                            disabled={isSaved}
                            className={`delete-toggle-button ${isDeleted ? 'deleted' : ''}`}>
                            {isDeleted ? 'Unmark Delete (d)' : 'Mark Delete (d)'}
                        </button>
                        <button onClick={() => navigate(1)}>→ (k)</button>
                        <button onClick={() => setIsFullscreen(false)} className="fullscreen-exit">Exit Fullscreen (f / Esc)</button>
                    </div>
                </div>
            )}
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
            <RenameModal
                isOpen={showRenameModal}
                onClose={() => setShowRenameModal(false)}
                onConfirm={handleRenameDirectory}
                initialValue={currentDirectory}
                isBusy={isRenaming}
            />

            <div className={`bottom-left-controls ${isSidebarCollapsed ? 'collapsed' : ''}`}>
                <button
                    className="sidebar-toggle"
                    onClick={() => setIsSidebarCollapsed(!isSidebarCollapsed)}
                    title={isSidebarCollapsed ? "Expand Sidebar" : "Collapse Sidebar"}
                >
                    {isSidebarCollapsed ? '→' : '←'}
                </button>
                <div className="sidebar-controls">
                    {filteredPhotos.length > 0 && currentPhotoName && (
                        <div className="photo-info-sidebar">
                            <p>{currentIndex + 1} / {filteredPhotos.length}</p>
                            <p className={`status ${isSaved ? 'status-saved' : (isSelected ? 'status-selected' : (isDeleted ? 'status-deleted' : ''))}`}>
                                {isSaved ? 'SAVED' : (isSelected ? 'SELECTED' : (isDeleted ? 'MARKED FOR DELETION' : 'Not Selected'))}
                            </p>
                        </div>
                    )}
                    <div className="date-range-container">
                        <div className="date-picker-container">
                            <label htmlFor="since-date">From:</label>
                            <input
                                type="date"
                                id="since-date"
                                value={sinceDate}
                                onChange={e => setSinceDate(e.target.value)}
                                className="date-picker"
                                max={untilDate || undefined}
                            />
                        </div>
                        <div className="date-picker-container">
                            <label htmlFor="until-date">To:</label>
                            <input
                                type="date"
                                id="until-date"
                                value={untilDate}
                                onChange={e => setUntilDate(e.target.value)}
                                className="date-picker"
                                min={sinceDate || undefined}
                            />
                        </div>
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
                    <div className="checkbox-container">
                        <label>
                            <input
                                type="checkbox"
                                checked={importRaws}
                                onChange={e => setImportRaws(e.target.checked)}
                            />
                            <span>Import RAWs (.CR3, .ORF)</span>
                        </label>
                    </div>

                    {/* Import Preview */}
                    {isLoadingPreview ? (
                        <div className="import-preview loading">
                            <p>Loading preview...</p>
                        </div>
                    ) : importPreview && importPreview.usb_connected ? (
                        <div className="import-preview">
                            {importPreview.error ? (
                                <p className="preview-error">{importPreview.error}</p>
                            ) : (
                                <>
                                    <div className="preview-stat main">
                                        <span className="preview-label">Will import:</span>
                                        <span className="preview-value">{importPreview.files_to_import} photos</span>
                                    </div>
                                    {importPreview.daily_breakdown && Object.keys(importPreview.daily_breakdown).length > 0 && (
                                        <div className="daily-breakdown">
                                            {Object.entries(importPreview.daily_breakdown)
                                                .sort(([a], [b]) => a.localeCompare(b))
                                                .map(([date, count]) => (
                                                    <div key={date} className="preview-stat daily">
                                                        <span className="preview-label">{date}:</span>
                                                        <span className="preview-value">{count} photos</span>
                                                    </div>
                                                ))}
                                        </div>
                                    )}
                                    {importPreview.skipped_duplicates > 0 && (
                                        <div className="preview-stat">
                                            <span className="preview-label">Will skip (duplicates):</span>
                                            <span className="preview-value">{importPreview.skipped_duplicates}</span>
                                        </div>
                                    )}
                                    {importPreview.skipped_by_date > 0 && (
                                        <div className="preview-stat">
                                            <span className="preview-label">Will skip (date filter):</span>
                                            <span className="preview-value">{importPreview.skipped_by_date}</span>
                                        </div>
                                    )}
                                    {importPreview.skipped_videos > 0 && (
                                        <div className="preview-stat">
                                            <span className="preview-label">Will skip (videos):</span>
                                            <span className="preview-value">{importPreview.skipped_videos}</span>
                                        </div>
                                    )}
                                    {importPreview.skipped_raws > 0 && (
                                        <div className="preview-stat">
                                            <span className="preview-label">Will skip (RAWs):</span>
                                            <span className="preview-value">{importPreview.skipped_raws}</span>
                                        </div>
                                    )}
                                    <div className="preview-stat">
                                        <span className="preview-label">Total on USB:</span>
                                        <span className="preview-value">{importPreview.total_files}</span>
                                    </div>
                                </>
                            )}
                        </div>
                    ) : (
                        <div className="import-preview error">
                            <p>USB not detected</p>
                        </div>
                    )}
                    <div className="folder-name-container">
                        <label htmlFor="new-folder-name">Import to folder:</label>
                        <input
                            type="text"
                            id="new-folder-name"
                            value={addToCurrentBatch ? currentDirectory : newFolderName}
                            onChange={e => { setFolderNameEdited(true); setNewFolderName(e.target.value); }}
                            onFocus={() => setFolderNameEdited(true)}
                            className="folder-name-input"
                            disabled={addToCurrentBatch}
                            spellCheck={false}
                        />
                    </div>
                    <button onClick={handleImport} disabled={isImporting} className="import-button">
                        {isImporting ? 'Importing...' : 'Import'}
                    </button>
                    {isImporting && importProgress && importProgress.total > 0 && (
                        <div className="import-progress">
                            <div className="import-progress-track">
                                <div
                                    className="import-progress-fill"
                                    style={{ width: `${Math.round((importProgress.copied / importProgress.total) * 100)}%` }}
                                />
                            </div>
                            <div className="import-progress-label">
                                {importProgress.copied} / {importProgress.total} files
                            </div>
                        </div>
                    )}
                </div>

                <div className="sidebar-controls">
                    {directories.length > 0 && (
                        <select
                            value={currentDirectory}
                            onChange={e => switchDirectory(e.target.value)}
                            className="directory-selector"
                        >
                            {directories.map(dir => (
                                <option key={dir} value={dir}>{dir}</option>
                            ))}
                        </select>
                    )}
                    {currentDirectory && (
                        <button
                            onClick={() => setShowRenameModal(true)}
                            disabled={isRenaming}
                            className="rename-button"
                        >
                            Rename Folder
                        </button>
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



            <main className="App-main">
                {photos.length > 0 ? (
                    <>
                        {showThumbnailView ? (
                            <div className="thumbnail-view-section">
                                <div className="thumbnail-view-header">
                                    <select
                                        value={carouselFilter}
                                        onChange={e => setCarouselFilter(e.target.value)}
                                        className="carousel-filter-select"
                                    >
                                        <option value="all">All Images ({filterCounts.all})</option>
                                        <option value="selected">Selected Only ({filterCounts.selected})</option>
                                        <option value="deleted">Marked for Deletion ({filterCounts.deleted})</option>
                                    </select>
                                    <span className="thumbnail-view-count">{filteredPhotos.length} photo{filteredPhotos.length !== 1 ? 's' : ''}</span>
                                </div>
                                {filteredPhotos.length > 0 ? (
                                    <ThumbnailGrid
                                        photos={filteredPhotos}
                                        currentIndex={currentIndex}
                                        setCurrentIndex={(index) => {
                                            setCurrentIndex(index);
                                            setShowThumbnailView(false);
                                        }}
                                        currentDirectory={currentDirectory}
                                        selectedPhotos={selectedPhotos}
                                        savedPhotos={savedPhotos}
                                        deletedPhotos={deletedPhotos}
                                    />
                                ) : (
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
                                )}
                            </div>
                        ) : (
                            <>
                                {filteredPhotos.length > 0 ? (
                                    <div className="main-photo-area">
                                        {currentPhotoName && (
                                            <div className="photo-filename-overlay">
                                                <div className="filename">{currentPhotoName}</div>
                                                <div className="photo-position-overlay">{currentIndex + 1} / {filteredPhotos.length}</div>
                                                {metadataParts.length > 0 && (
                                                    <div className="photo-metadata-overlay">{metadataParts.join(' · ')}</div>
                                                )}
                                            </div>
                                        )}
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
                                                />
                                            </div>
                                        ) : (
                                            <PhotoViewer
                                                photoName={currentPhotoName}
                                                directory={currentDirectory}
                                                isSelected={isSelected}
                                                isSaved={isSaved}
                                                isDeleted={isDeleted}
                                            />
                                        )}
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
                                            <option value="all">All Images ({filterCounts.all})</option>
                                            <option value="selected">Selected Only ({filterCounts.selected})</option>
                                            <option value="deleted">Marked for Deletion ({filterCounts.deleted})</option>
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
                                    {currentPhotoName && (
                                        <div className="carousel-filename-container">
                                            <span className="carousel-filename">{currentPhotoName}</span>
                                        </div>
                                    )}
                                </div>
                            </>
                        )}
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
                    <button
                        onClick={() => setShowThumbnailView(!showThumbnailView)}
                        disabled={photos.length === 0}
                        className={`thumbnail-view-button ${showThumbnailView ? 'active' : ''}`}
                    >
                        {showThumbnailView ? 'Carousel View' : 'Thumbnail View'}
                    </button>
                    <button
                        onClick={() => { setPinnedPhoto(null); setIsFullscreen(true); }}
                        disabled={!currentPhotoName || showThumbnailView}
                        className="fullscreen-button"
                    >
                        Fullscreen (f)
                    </button>
                    <button onClick={handleSave} disabled={selectedPhotos.size === 0} className="save-button">
                        Save {selectedPhotos.size} new selections
                    </button>
                    <button
                        onClick={handleExportRaw}
                        disabled={exportStatus.selected_count === 0 || isExportingRaw}
                        className="export-raw-button">
                        {isExportingRaw ? 'Exporting...' : `Export Raw Files (${exportStatus.missing_count} missing)`}
                    </button>
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
                    <p>Use 's' to select, 'x' to unselect, 'd' to mark for deletion, 'h' to pin/unpin, and 'f' to toggle fullscreen. Press 'Escape' to exit fullscreen or clear pinned photo.</p>
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
                            src={`${API_URL}/thumbnail/${encodeURIComponent(currentDirectory)}/${encodeURIComponent(photoName)}`}
                            alt={`thumbnail-${photoName}`}
                        />
                    </div>
                );
            })}
        </div>
    );
}

function ThumbnailGrid({ photos, currentIndex, setCurrentIndex, currentDirectory, selectedPhotos, savedPhotos, deletedPhotos }) {
    return (
        <div className="thumbnail-grid">
            {photos.map((photoName, index) => {
                const isSelected = selectedPhotos.has(photoName);
                const isSaved = savedPhotos.has(photoName);
                const isDeleted = deletedPhotos.has(photoName);
                return (
                    <div
                        key={photoName}
                        className={`thumbnail-grid-item ${index === currentIndex ? 'active' : ''} ${isSaved ? 'saved' : (isDeleted ? 'deleted' : (isSelected ? 'selected' : ''))}`}
                        onClick={() => setCurrentIndex(index)}
                        title={photoName}
                    >
                        <img
                            src={`${API_URL}/thumbnail/${encodeURIComponent(currentDirectory)}/${encodeURIComponent(photoName)}`}
                            alt={photoName}
                            loading="lazy"
                        />
                        <div className="thumbnail-grid-label">{photoName}</div>
                    </div>
                );
            })}
        </div>
    );
}

export default App;
