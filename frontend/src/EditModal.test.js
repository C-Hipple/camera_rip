import { buildToneLUT, normalizedAspect, fitCropToAspect, rectFromAnchor } from './EditModal';

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

// The crop overlay works in frame fractions while the ratios the user picks are
// in pixels, so every case below checks the pixel shape that comes back out.
const pixelRatio = (box, width, height) => (box.w * width) / (box.h * height);

describe('the locked crop ratio', () => {
  test('a preset matching the photo leaves the frame shape alone', () => {
    expect(normalizedAspect('original', 3000, 2000)).toBe(1);
    expect(normalizedAspect('3:2', 3000, 2000)).toBeCloseTo(1, 10);
  });

  test('presets take a portrait photo the tall way round', () => {
    // 3:2 on an upright shot is a 2:3 upright crop, not a sideways one.
    expect(normalizedAspect('3:2', 2000, 3000)).toBeCloseTo(1, 10);
    expect(normalizedAspect('16:9', 2000, 3000)).toBeCloseTo(0.84375, 10);
  });

  test('there is no ratio to hold before the photo has loaded', () => {
    expect(normalizedAspect('1:1', 0, 0)).toBeNull();
    expect(normalizedAspect('nonsense', 3000, 2000)).toBeNull();
  });

  test('the full frame snaps to the widest box the ratio allows', () => {
    const square = fitCropToAspect({ x: 0, y: 0, w: 1, h: 1 }, normalizedAspect('1:1', 3000, 2000));
    expect(pixelRatio(square, 3000, 2000)).toBeCloseTo(1, 10);
    expect(square.h).toBe(1); // the short side is what runs out first
    expect(square.w).toBeCloseTo(2 / 3, 10);
    expect(square.x).toBeCloseTo(1 / 6, 10); // and it lands centred

    const wide = fitCropToAspect({ x: 0, y: 0, w: 1, h: 1 }, normalizedAspect('16:9', 4000, 3000));
    expect(pixelRatio(wide, 4000, 3000)).toBeCloseTo(16 / 9, 10);
    expect(wide.w).toBe(1);
  });

  test('snapping shrinks a crop rather than growing it, and stays in frame', () => {
    const box = { x: 0.6, y: 0.5, w: 0.4, h: 0.5 };
    const snapped = fitCropToAspect(box, normalizedAspect('1:1', 3000, 2000));
    expect(snapped.w).toBeLessThanOrEqual(box.w);
    expect(snapped.h).toBeLessThanOrEqual(box.h);
    expect(snapped.x).toBeGreaterThanOrEqual(0);
    expect(snapped.y).toBeGreaterThanOrEqual(0);
    expect(snapped.x + snapped.w).toBeLessThanOrEqual(1);
    expect(snapped.y + snapped.h).toBeLessThanOrEqual(1);
    expect(pixelRatio(snapped, 3000, 2000)).toBeCloseTo(1, 10);
  });

  test('an unlocked drag still spans exactly the two corners', () => {
    const box = rectFromAnchor({ x: 0.2, y: 0.8 }, { x: 0.7, y: 0.3 }, null);
    expect(box.x).toBeCloseTo(0.2, 10);
    expect(box.y).toBeCloseTo(0.3, 10);
    expect(box.w).toBeCloseTo(0.5, 10);
    expect(box.h).toBeCloseTo(0.5, 10);
  });

  test('a locked drag holds the ratio whichever way it is pulled', () => {
    const aspect = normalizedAspect('1:1', 3000, 2000);
    const down = rectFromAnchor({ x: 0.1, y: 0.1 }, { x: 0.5, y: 0.9 }, aspect);
    expect(pixelRatio(down, 3000, 2000)).toBeCloseTo(1, 10);
    expect(down.x).toBeCloseTo(0.1, 10);
    expect(down.y).toBeCloseTo(0.1, 10);

    // Dragging back up and to the left pivots around the anchor instead.
    const up = rectFromAnchor({ x: 0.9, y: 0.9 }, { x: 0.5, y: 0.5 }, aspect);
    expect(pixelRatio(up, 3000, 2000)).toBeCloseTo(1, 10);
    expect(up.x + up.w).toBeCloseTo(0.9, 10);
    expect(up.y + up.h).toBeCloseTo(0.9, 10);
  });

  test('a locked drag into the corner stops at the ratio instead of breaking it', () => {
    const aspect = normalizedAspect('1:1', 3000, 2000);
    const box = rectFromAnchor({ x: 0.5, y: 0.5 }, { x: 1, y: 1 }, aspect);
    expect(pixelRatio(box, 3000, 2000)).toBeCloseTo(1, 10);
    expect(box.x + box.w).toBeLessThanOrEqual(1);
    expect(box.y + box.h).toBeLessThanOrEqual(1);
    expect(box.h).toBeCloseTo(0.5, 10); // the bottom edge is the limit here
  });
});
