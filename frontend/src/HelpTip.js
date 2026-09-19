import React, { useId, useState } from 'react';
import './HelpTip.css';

// A "?" that sits beside a control and explains it on hover, on focus, or on a
// tap. The bubble is positioned against the row the tip lives in rather than
// against the button itself — the row is the widest box that is certainly on
// screen, so the text can never land off the edge of a narrow modal. Rows that
// host a tip therefore carry `position: relative` (see EditModal.css).
function HelpTip({ label, align = 'left', children }) {
    const [open, setOpen] = useState(false);
    const tipId = useId();

    return (
        <button
            type="button"
            className={`help-tip ${open ? 'open' : ''}`}
            // The label names the control, not the "?", which says nothing on
            // its own. It deliberately avoids repeating the control's own label
            // verbatim so the two stay tellable apart.
            aria-label={`About ${label}`}
            aria-describedby={open ? tipId : undefined}
            onMouseEnter={() => setOpen(true)}
            onMouseLeave={() => setOpen(false)}
            onFocus={() => setOpen(true)}
            onBlur={() => setOpen(false)}
            // Touch has no hover, so a tap toggles. Mouse clicks are left alone:
            // the bubble is already up from the hover, and closing it under a
            // pointer that never moved would just read as broken.
            onPointerDown={(e) => { if (e.pointerType !== 'mouse') setOpen(o => !o); }}
            onKeyDown={(e) => {
                if (e.key !== 'Escape' || !open) return;
                // Escape closes the editor; while a bubble is up it takes the
                // key for itself and the editor stays where it was.
                e.stopPropagation();
                setOpen(false);
            }}
        >
            <span aria-hidden="true">?</span>
            {open && (
                <span className={`help-tip-bubble ${align}`} id={tipId} role="tooltip">
                    {children}
                </span>
            )}
        </button>
    );
}

export default HelpTip;
