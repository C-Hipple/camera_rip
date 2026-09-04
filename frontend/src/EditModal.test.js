import { buildToneLUT } from './EditModal';

// The preview canvas has to show what the backend will write, so the table
// below is asserted verbatim in backend-go/edit_test.go as well. If the two
// ever drift, one of these suites goes red.
test('the preview tone curve matches the backend reference values', () => {
  const cases = [
    { exposure: 1, black: 0, input: 128, want: 175 },
    { exposure: -1, black: 0, input: 128, want: 93 },
    { exposure: 0, black: 50, input: 128, want: 110 },
    { exposure: 0, black: -50, input: 0, want: 28 },
    { exposure: 0.5, black: 25, input: 200, want: 233 },
    { exposure: 1, black: 0, input: 64, want: 88 },
  ];
  cases.forEach(({ exposure, black, input, want }) => {
    expect(buildToneLUT(exposure, black)[input]).toBe(want);
  });
});

test('neutral settings leave every value untouched', () => {
  const lut = buildToneLUT(0, 0);
  for (let i = 0; i < 256; i++) {
    expect(lut[i]).toBe(i);
  }
});

test('the black level moves the shadows and leaves white alone', () => {
  expect(buildToneLUT(0, 100)[255]).toBe(255);
  expect(buildToneLUT(0, -100)[255]).toBe(255);
  expect(buildToneLUT(0, 100)[40]).toBe(0);
  expect(buildToneLUT(0, -100)[0]).toBeGreaterThan(0);
});

test('raising exposure brightens without blowing out white or lifting black', () => {
  const brighter = buildToneLUT(1, 0);
  expect(brighter[128]).toBeGreaterThan(128);
  expect(brighter[255]).toBe(255);
  expect(brighter[0]).toBe(0);
  expect(buildToneLUT(-1, 0)[128]).toBeLessThan(128);
});
