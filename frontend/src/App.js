import React, { useState, useEffect, useCallback } from 'react';
import { ToastContainer, toast } from 'react-toastify';
import 'react-toastify/dist/ReactToastify.css';
import './App.css';
import PhotoViewer from './PhotoViewer';

const API_URL = process.env.REACT_APP_API_URL || 'http://localhost:5001';

function App() {
    const [directories, setDirectories] = useState([]);
    const [currentDirectory, setCurrentDirectory] = useState('');
    const [photos, setPhotos] = useState([]);
    const [currentIndex, setCurrentIndex] = useState(0);
    const [selectedPhotos, setSelectedPhotos] = useState(new Set());
    const [isImporting, setIsImporting] = useState(false);
    const [sinceDate, setSinceDate] = useState('');
    const [pinnedPhoto, setPinnedPhoto] = useState(null);

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
                body: JSON.stringify({ since: sinceDate })
            });
            const data = await response.json();
            if (response.ok) {
                toast.update(toastId, { render: data.message, type: "success", isLoading: false, autoClose: 5000 });
                if (data.new_directory) {
                    fetchDirectories();
                    setCurrentDirectory(data.new_directory);
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
                    setSelectedPhotos(new Set());
                }
            })
            .catch(err => toast.error("Error fetching photos."));
    }, [currentDirectory]);

    const handleSelection = useCallback((photoName, select) => {
        setSelectedPhotos(prevSelected => {
            const newSelected = new Set(prevSelected);
            if (select) {
                newSelected.add(photoName);
            } else {
                newSelected.delete(photoName);
            }
            return newSelected;
        });
    }, []);

    const handleSave = () => {
        const toastId = toast.loading("Saving...")
        fetch(`${API_URL}/api/save`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify({
                directory: currentDirectory,
                selected_files: Array.from(selectedPhotos),
            }),
        })
        .then(res => res.json())
        .then(data => {
            if (data.error) {
                toast.update(toastId, { render: data.error, type: "error", isLoading: false, autoClose: 5000 });
            } else {
                toast.update(toastId, { render: data.message, type: "success", isLoading: false, autoClose: 5000 });
            }
        })
        .catch(err => {
            toast.update(toastId, { render: "An error occurred while saving.", type: "error", isLoading: false, autoClose: 5000 });
        });
    };

    const navigate = (direction) => {
        if (photos.length === 0) return;
        const newIndex = (currentIndex + direction + photos.length) % photos.length;
        setCurrentIndex(newIndex);
    };

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
    const isPinnedSelected = selectedPhotos.has(pinnedPhoto);

    return (
        <div className="App">
            <ToastContainer position="bottom-center" autoClose={5000} hideProgressBar={false} newestOnTop={false} closeOnClick rtl={false} pauseOnFocusLoss draggable pauseOnHover theme="dark" />

            <div className="bottom-left-controls">
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
                                    >
                                        <p>{pinnedPhoto}</p>
                                        <p className={`status ${isPinnedSelected ? 'status-selected' : ''}`}>
                                            {isPinnedSelected ? 'SELECTED' : 'Not Selected'}
                                        </p>
                                        <p className="status status-pinned">PINNED</p>
                                    </PhotoViewer>
                                    <PhotoViewer
                                        photoName={currentPhotoName}
                                        directory={currentDirectory}
                                        isSelected={isSelected}
                                    >
                                        <p>{currentIndex + 1} / {photos.length}</p>
                                        <p>{currentPhotoName}</p>
                                        <p className={`status ${isSelected ? 'status-selected' : ''}`}>
                                            {isSelected ? 'SELECTED' : 'Not Selected'}
                                        </p>
                                    </PhotoViewer>
                                </div>
                            ) : (
                                <PhotoViewer
                                    photoName={currentPhotoName}
                                    directory={currentDirectory}
                                    isSelected={isSelected}
                                >
                                    <p>{currentIndex + 1} / {photos.length}</p>
                                    <p>{currentPhotoName}</p>
                                    <p className={`status ${isSelected ? 'status-selected' : ''}`}>
                                        {isSelected ? 'SELECTED' : 'Not Selected'}
                                    </p>
                                </PhotoViewer>
                            )}
                        </div>
                        <Carousel
                            photos={photos}
                            currentIndex={currentIndex}
                            setCurrentIndex={setCurrentIndex}
                            currentDirectory={currentDirectory}
                            selectedPhotos={selectedPhotos}
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
                        disabled={photos.length === 0}
                        className={`select-toggle-button ${isSelected ? 'selected' : ''}`}>
                        {isSelected ? 'Unselect (x)' : 'Select (s)'}
                    </button>
                    <button onClick={() => navigate(1)} disabled={photos.length === 0}>Next (→ or k)</button>
                    <button onClick={handleSave} disabled={selectedPhotos.size === 0} className="save-button">
                        Save {selectedPhotos.size} selected photos
                    </button>
                </div>
                <div className="instructions">
                    <p>Use 's' to select, 'x' to unselect, and 'h' to pin/unpin. Press 'Escape' to clear pinned photo.</p>
                </div>
            </main>
        </div>
    );
}

function Carousel({ photos, currentIndex, setCurrentIndex, currentDirectory, selectedPhotos }) {
    const getCarouselPhotos = () => {
        const numPhotos = photos.length;
        console.log("length: ", photos.length);
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
                console.log(photoIndex)
                const photoName = photos[photoIndex];
                const isSelected = selectedPhotos.has(photoName);
                return (
                    <div
                        key={i}
                        className={`carousel-thumbnail ${photoIndex === currentIndex ? 'active' : ''} ${isSelected ? 'selected' : ''}`}
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
