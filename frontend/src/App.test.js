import { render, screen, fireEvent } from '@testing-library/react';
import App from './App';

const originalFetch = global.fetch;
afterEach(() => {
  localStorage.clear();
  global.fetch = originalFetch;
});

const mockApi = ({ photos = [], saved = [] } = {}) => {
  const jsonResponse = (data) => Promise.resolve({ ok: true, json: () => Promise.resolve(data) });
  global.fetch = jest.fn((url) => {
    const path = String(url);
    if (path.includes('/api/directories')) return jsonResponse(['session-1']);
    if (path.includes('/api/photos?')) return jsonResponse(photos);
    if (path.includes('/api/selected-photos')) return jsonResponse(saved);
    if (path.includes('/api/export-status')) return jsonResponse({ selected_count: saved.length, raw_count: 0, missing_count: 0 });
    return jsonResponse({});
  });
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
