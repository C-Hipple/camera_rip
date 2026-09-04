import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import App from './App';

const originalFetch = global.fetch;
afterEach(() => {
  localStorage.clear();
  global.fetch = originalFetch;
  // jsdom has no matchMedia; tests that fake a phone viewport define it
  delete window.matchMedia;
});

// jsdom leaves matchMedia undefined, so the app renders desktop by default.
// Defining it with matches: true switches the app to the mobile layout.
const fakeMobileViewport = () => {
  window.matchMedia = (query) => ({
    matches: true,
    media: query,
    addEventListener: () => { },
    removeEventListener: () => { },
  });
};

const mockApi = ({ photos = [], saved = [], gallery = null, upload = null, directories = null } = {}) => {
  const jsonResponse = (data, ok = true) => Promise.resolve({ ok, json: () => Promise.resolve(data) });
  const sessions = directories || [{ name: 'session-1', photo_count: photos.length, selected_count: saved.length }];
  global.fetch = jest.fn((url) => {
    const path = String(url);
    if (path.includes('/api/directories')) return jsonResponse(sessions);
    if (path.includes('/api/photos?')) return jsonResponse(photos);
    if (path.includes('/api/selected-photos')) return jsonResponse(saved);
    if (path.includes('/api/export-status')) return jsonResponse({ selected_count: saved.length, raw_count: 0, missing_count: 0 });
    if (path.includes('/api/gallery-config')) return jsonResponse(gallery || { configured: false, base_url: '' });
    if (path.includes('/api/gallery-upload')) return jsonResponse(upload || { status: 'uploaded', url: '' });
    return jsonResponse({});
  });
};

// The body of the nth POST the app made to the given endpoint.
const postedTo = (endpoint) => {
  const call = global.fetch.mock.calls.find(([url]) => String(url).includes(endpoint));
  return call ? JSON.parse(call[1].body) : null;
};

test('renders photo selector app', () => {
  render(<App />);
  const heading = screen.getByText(/Photo Selector/i);
  expect(heading).toBeInTheDocument();
});

test('renders import button', () => {
  render(<App />);
  const importButton = screen.getByRole('button', { name: /^Import$/i });
  expect(importButton).toBeInTheDocument();
  expect(importButton).not.toBeDisabled();
});

test('renders navigation buttons in disabled state when no photos', () => {
  render(<App />);
  const prevButton = screen.getByRole('button', { name: /Previous/i });
  const nextButton = screen.getByRole('button', { name: /Next/i });
  expect(prevButton).toBeDisabled();
  expect(nextButton).toBeDisabled();
});

test('restores unsaved selections and deletion marks from localStorage', async () => {
  localStorage.setItem('camera-rip.pending.session-1', JSON.stringify({
    selected: ['100_IMG_0001.JPG', 'gone.JPG', 'already-saved.JPG'],
    deleted: ['100_IMG_0002.JPG'],
  }));
  mockApi({
    photos: ['100_IMG_0001.JPG', '100_IMG_0002.JPG', '100_IMG_0003.JPG', 'already-saved.JPG'],
    saved: ['already-saved.JPG'],
  });
  render(<App />);
  // One pending selection survives: gone.JPG no longer exists and
  // already-saved.JPG is on disk already, so both are dropped.
  expect(await screen.findByRole('button', { name: /Save 1 new selections/i })).toBeInTheDocument();
  expect(await screen.findByRole('option', { name: /Marked for Deletion \(1\)/i })).toBeInTheDocument();
});

test('directory selector labels each session with selected / total counts', async () => {
  mockApi({
    photos: ['100_IMG_0001.JPG'],
    directories: [
      { name: '2025-12-11 Holiday Party', photo_count: 200, selected_count: 12 },
      { name: '2025-11-02 Hike', photo_count: 40, selected_count: 0 },
    ],
  });
  render(<App />);
  expect(await screen.findByRole('option', { name: '2025-12-11 Holiday Party (12 / 200)' })).toBeInTheDocument();
  expect(screen.getByRole('option', { name: '2025-11-02 Hike (0 / 40)' })).toBeInTheDocument();
  // The first session is the one opened, and its photos were requested by name.
  expect(global.fetch).toHaveBeenCalledWith(expect.stringContaining('/api/photos?directory=2025-12-11%20Holiday%20Party'));
});

test('mobile: double-tap selects the photo and the action bar unselects it', async () => {
  fakeMobileViewport();
  mockApi({ photos: ['100_IMG_0001.JPG', '100_IMG_0002.JPG'] });
  const { container } = render(<App />);
  await screen.findByRole('option', { name: /All Images \(2\)/i });
  expect(screen.getByText(/Double-tap photo to select/i)).toBeInTheDocument();

  const photoArea = container.querySelector('.main-photo-area');
  const tap = () => {
    fireEvent.touchStart(photoArea, { touches: [{ clientX: 100, clientY: 100 }] });
    fireEvent.touchEnd(photoArea, { changedTouches: [{ clientX: 100, clientY: 100 }] });
  };

  // A single tap must not select
  tap();
  expect(container.querySelector('.mobile-selected-panel')).toBeNull();

  // The second tap completes the double-tap and opens the selected panel
  tap();
  await waitFor(() => expect(container.querySelector('.mobile-selected-panel')).toBeInTheDocument());
  expect(screen.getByRole('button', { name: /Save \(1\)/i })).toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', { name: '✕ Unselect' }));
  await waitFor(() => expect(container.querySelector('.mobile-selected-panel')).toBeNull());
  expect(screen.getByText(/Double-tap photo to select/i)).toBeInTheDocument();
});

test('stashes selections to localStorage as they are made', async () => {
  mockApi({ photos: ['100_IMG_0001.JPG', '100_IMG_0002.JPG'] });
  render(<App />);
  // Wait for the photo list to load before using a keyboard shortcut
  await screen.findByRole('option', { name: /All Images \(2\)/i });
  fireEvent.keyDown(window, { key: 's' });
  await screen.findByRole('button', { name: /Save 1 new selections/i });
  expect(JSON.parse(localStorage.getItem('camera-rip.pending.session-1'))).toEqual({
    selected: ['100_IMG_0001.JPG'],
    deleted: [],
  });
});

test('gallery upload button is hidden until a photo is selected, and posts title and hashtags', async () => {
  mockApi({
    photos: ['100_IMG_0001.JPG', '100_IMG_0002.JPG'],
    gallery: { configured: true, base_url: 'https://gallery.example' },
    upload: { status: 'uploaded', url: 'https://gallery.example/p/42' },
  });
  render(<App />);
  await screen.findByRole('option', { name: /All Images \(2\)/i });

  // Nothing selected yet, so there is nothing to upload
  expect(screen.queryByRole('button', { name: /Upload to Gallery/i })).not.toBeInTheDocument();

  fireEvent.keyDown(window, { key: 's' });
  const uploadButton = await screen.findByRole('button', { name: /Upload to Gallery/i });
  fireEvent.click(uploadButton);

  fireEvent.change(await screen.findByLabelText(/Title/i), { target: { value: 'Sunrise' } });
  fireEvent.change(screen.getByLabelText(/Hashtags/i), { target: { value: '#film #mono' } });
  fireEvent.click(screen.getByRole('button', { name: /^Upload$/i }));

  await waitFor(() => expect(postedTo('/api/gallery-upload')).toEqual({
    directory: 'session-1',
    filename: '100_IMG_0001.JPG',
    title: 'Sunrise',
    tags: '#film #mono',
  }));
  // A successful upload closes the modal
  await waitFor(() => expect(screen.queryByLabelText(/Hashtags/i)).not.toBeInTheDocument());
});

test('gallery upload button stays hidden when the backend has no gallery configured', async () => {
  mockApi({ photos: ['100_IMG_0001.JPG'], gallery: { configured: false, base_url: '' } });
  render(<App />);
  await screen.findByRole('option', { name: /All Images \(1\)/i });
  fireEvent.keyDown(window, { key: 's' });
  await screen.findByRole('button', { name: /Save 1 new selections/i });
  expect(screen.queryByRole('button', { name: /Upload to Gallery/i })).not.toBeInTheDocument();
});
