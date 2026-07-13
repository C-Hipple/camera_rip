import React, { useState, useEffect } from 'react';
import './ConfirmModal.css';

function RenameModal({ isOpen, onClose, onConfirm, initialValue, isBusy }) {
    const [value, setValue] = useState(initialValue || '');

    useEffect(() => {
        if (isOpen) {
            setValue(initialValue || '');
        }
    }, [isOpen, initialValue]);

    if (!isOpen) return null;

    const submit = () => {
        if (!isBusy) {
            onConfirm(value);
        }
    };

    return (
        <div className="modal-overlay" onClick={onClose}>
            <div className="modal-content" onClick={(e) => e.stopPropagation()}>
                <h2 className="modal-title">Rename Folder</h2>
                <input
                    type="text"
                    className="modal-input"
                    value={value}
                    onChange={(e) => setValue(e.target.value)}
                    onKeyDown={(e) => {
                        e.stopPropagation();
                        if (e.key === 'Enter') {
                            submit();
                        } else if (e.key === 'Escape') {
                            onClose();
                        }
                    }}
                    autoFocus
                />
                <div className="modal-buttons">
                    <button
                        className="modal-button modal-button-cancel"
                        onClick={onClose}
                    >
                        Cancel
                    </button>
                    <button
                        className="modal-button modal-button-confirm"
                        onClick={submit}
                        disabled={isBusy}
                    >
                        {isBusy ? 'Renaming...' : 'Rename'}
                    </button>
                </div>
            </div>
        </div>
    );
}

export default RenameModal;
