import { render, screen } from '@testing-library/react';
import App from './App';

test('renders photo selector app', () => {
  render(<App />);
  const heading = screen.getByText(/Photo Selector/i);
  expect(heading).toBeInTheDocument();
});

test('renders import button', () => {
  render(<App />);
  const importButton = screen.getByRole('button', { name: /Import/i });
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
