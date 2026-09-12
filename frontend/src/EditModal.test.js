import {
  buildToneLUT, applyHighlights, skyLumWeight, skyGradient, skyPullStops,
  normalizedAspect, fitCropToAspect, rectFromAnchor,
} from './EditModal';

// The preview canvas has to show what the backend will write, so the table
// below is asserted verbatim in backend-go/edit_test.go as well. If the two
// ever drift, one of these suites goes red.
test('the preview tone curve matches the backend reference values', () => {
  const cases = [
    { exposure: 1, black: 0, highlights: 0, skyPull: 0, input: 128, want: 175 },
    { exposure: -1, black: 0, highlights: 0, skyPull: 0, input: 128, want: 93 },
    { exposure: 0, black: 50, highlights: 0, skyPull: 0, input: 128, want: 110 },
    { exposure: 0, black: -50, highlights: 0, skyPull: 0, input: 0, want: 28 },
    { exposure: 0.5, black: 25, highlights: 0, skyPull: 0, input: 200, want: 233 },
    { exposure: 1, black: 0, highlights: 0, skyPull: 0, input: 64, want: 88 },
    // Highlight recovery: a bright value rolls down, and pure white with it.
    { exposure: 0, black: 0, highlights: -100, skyPull: 0, input: 200, want: 183 },
    { exposure: 0, black: 0, highlights: -100, skyPull: 0, input: 255, want: 208 },
    // The pair the bird case turns on — +1.5 stops clips 200 to white, and the
    // shoulder brings it back under with room to spare.
    { exposure: 1.5, black: 0, highlights: 0, skyPull: 0, input: 200, want: 255 },
    { exposure: 1.5, black: 0, highlights: -80, skyPull: 0, input: 200, want: 235 },
    // A sky pull darkens what is already bright and ignores what is not.
    { exposure: 0, black: 0, highlights: 0, skyPull: 1, input: 230, want: 168 },
    { exposure: 0, black: 0, highlights: 0, skyPull: 2, input: 230, want: 122 },
    { exposure: 0, black: 0, highlights: 0, skyPull: 2, input: 60, want: 60 },
    // And all four together, the way the sky-balance sliders stack them.
    { exposure: 1.5, black: 0, highlights: -60, skyPull: 1.2, input: 210, want: 216 },
  ];
  cases.forEach(({ exposure, black, highlights, skyPull, input, want }) => {
    expect(buildToneLUT(exposure, black, highlights, skyPull)[input]).toBe(want);
  });
});

test('neutral settings leave every value untouched', () => {
  const lut = buildToneLUT(0, 0, 0, 0);
  for (let i = 0; i < 256; i++) {
    expect(lut[i]).toBe(i);
  }
});

test('the black level moves the shadows and leaves white alone', () => {
  expect(buildToneLUT(0, 100, 0, 0)[255]).toBe(255);
  expect(buildToneLUT(0, -100, 0, 0)[255]).toBe(255);
  expect(buildToneLUT(0, 100, 0, 0)[40]).toBe(0);
  expect(buildToneLUT(0, -100, 0, 0)[0]).toBeGreaterThan(0);
});

test('raising exposure brightens without blowing out white or lifting black', () => {
  const brighter = buildToneLUT(1, 0, 0, 0);
  expect(brighter[128]).toBeGreaterThan(128);
  expect(brighter[255]).toBe(255);
  expect(brighter[0]).toBe(0);
  expect(buildToneLUT(-1, 0, 0, 0)[128]).toBeLessThan(128);
});

// The sky tools exist for one shot: the exposure is up for the bird and the sky
// has gone white. Every case below is a piece of getting that sky back.
describe('the sky balance tools', () => {
  test('the highlight shoulder leaves the midtones alone and pulls white down', () => {
    [0, 0.25, 0.5].forEach(v => expect(applyHighlights(v, -1)).toBe(v));

    let previous = 0;
    [0.6, 0.8, 1.0, 1.5, 3.0, 6.0].forEach(v => {
      const got = applyHighlights(v, -1);
      expect(got).toBeLessThan(1);
      expect(got).toBeGreaterThan(previous);
      previous = got;
    });

    // It leaves the knee at slope 1, so there is no kink where it takes over.
    expect((applyHighlights(0.5 + 1e-6, -1) - 0.5) / 1e-6).toBeCloseTo(1, 3);

    // Pushing highlights up is the exact inverse of rolling them off.
    [0.55, 0.7, 0.9, 1.4].forEach(v => {
      expect(applyHighlights(applyHighlights(v, -0.6), 0.6)).toBeCloseTo(v, 9);
    });
    expect(applyHighlights(0.9, 0)).toBe(0.9);
  });

  test('recovery turns a clipped sky back into a rising ramp', () => {
    const lifted = buildToneLUT(1.5, 0, 0, 0);
    expect(lifted[190]).toBe(255);
    expect(lifted[255]).toBe(255);

    const recovered = buildToneLUT(1.5, 0, -80, 0);
    expect(recovered[255]).toBeLessThan(255);
    expect(recovered[255]).toBeGreaterThan(recovered[190]);
    // And the bird, down in the midtones, keeps the exposure it was given.
    expect(recovered[90]).toBe(lifted[90]);
  });

  test('the pull only reaches what is brighter than a midtone', () => {
    expect(skyLumWeight(0.3)).toBe(0);
    expect(skyLumWeight(0.45)).toBe(0);
    expect(skyLumWeight(0.8)).toBe(1);
    expect(skyLumWeight(0.92)).toBe(1);

    let previous = -1;
    for (let v = 0; v <= 1.0001; v += 0.05) {
      const got = skyLumWeight(v);
      expect(got).toBeGreaterThanOrEqual(previous);
      previous = got;
    }
  });

  test('the gradient covers the frame down to the horizon, then eases out', () => {
    [0, 50, 99].forEach(y => expect(skyGradient(y, 200, 50)).toBe(1));
    expect(skyGradient(125, 200, 50)).toBeGreaterThan(0);
    expect(skyGradient(125, 200, 50)).toBeLessThan(1);
    [150, 180, 199].forEach(y => expect(skyGradient(y, 200, 50)).toBe(0));

    // A horizon at the bottom edge pushes the feather off the frame, so the
    // pull is even everywhere — what a bird against nothing but sky needs.
    [0, 100, 199].forEach(y => expect(skyGradient(y, 200, 100)).toBe(1));

    let previous = 2;
    for (let y = 0; y < 200; y++) {
      const got = skyGradient(y, 200, 50);
      expect(got).toBeLessThanOrEqual(previous);
      previous = got;
    }

    expect(skyGradient(0, 0, 50)).toBe(0);
  });

  test('the slider only ever darkens, up to two stops', () => {
    expect(skyPullStops(0)).toBe(0);
    expect(skyPullStops(-30)).toBe(0);
    expect(skyPullStops(50)).toBeCloseTo(1, 10);
    expect(skyPullStops(100)).toBeCloseTo(2, 10);
    expect(skyPullStops(200)).toBeCloseTo(2, 10);
  });
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
