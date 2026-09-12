import {
  buildToneLUT, applyHighlights, applySkyPull, applyShadows,
  whiteBalanceGains, greyPointWhiteBalance, skyGradient, skyPullStops, shadowLift,
  normalizedAspect, fitCropToAspect, rectFromAnchor,
} from './EditModal';

// The preview canvas has to show what the backend will write, so the table
// below is asserted verbatim in backend-go/edit_test.go as well. If the two
// ever drift, one of these suites goes red.
test('the preview tone curve matches the backend reference values', () => {
  const cases = [
    { params: { exposure: 1 }, input: 128, want: 175 },
    { params: { exposure: -1 }, input: 128, want: 93 },
    { params: { black: 50 }, input: 128, want: 110 },
    { params: { black: -50 }, input: 0, want: 28 },
    { params: { exposure: 0.5, black: 25 }, input: 200, want: 233 },
    { params: { exposure: 1 }, input: 64, want: 88 },
    // Highlight recovery: a bright value rolls down, and pure white with it.
    { params: { highlights: -100 }, input: 200, want: 183 },
    { params: { highlights: -100 }, input: 255, want: 208 },
    // The pair the bird case turns on — +1.5 stops clips 200 to white, and the
    // shoulder brings it back under with room to spare.
    { params: { exposure: 1.5 }, input: 200, want: 255 },
    { params: { exposure: 1.5, highlights: -80 }, input: 200, want: 235 },
    // A sky pull darkens what is already bright and ignores what is not.
    { params: { skyPull: 1 }, input: 230, want: 184 },
    { params: { skyPull: 2 }, input: 230, want: 154 },
    { params: { skyPull: 2 }, input: 60, want: 60 },
    // The shadow lift is the mirror: it moves a dark value a long way and
    // leaves a bright one exactly alone.
    { params: { shadows: 1 }, input: 51, want: 88 },
    { params: { shadows: 0.5 }, input: 51, want: 69 },
    { params: { shadows: 1 }, input: 200, want: 200 },
    // A white balance gain is a per-channel exposure, so it lands where the
    // same number of stops on the exposure slider would.
    { params: { channelGain: 2 }, input: 128, want: 175 },
    { params: { channelGain: 0.5 }, input: 128, want: 93 },
    // And everything at once, the way the sliders stack them.
    { params: { exposure: 1.5, highlights: -60, skyPull: 1.2 }, input: 210, want: 222 },
  ];
  cases.forEach(({ params, input, want }) => {
    expect(buildToneLUT(params)[input]).toBe(want);
  });
});

test('neutral settings leave every value untouched', () => {
  const lut = buildToneLUT({});
  for (let i = 0; i < 256; i++) {
    expect(lut[i]).toBe(i);
  }
});

// The curve doubling back would render a bright patch darker than a dim one —
// a sky with its own gradient running backwards. The backend sweeps the same
// grid in TestToneCurveIsMonotone.
test('the curve never doubles back, whatever the sliders say', () => {
  [0, 1, 2].forEach(skyPull => {
    [-2, 0, 1.5].forEach(exposure => {
      [-100, 0, 100].forEach(highlights => {
        [0, 1].forEach(shadows => {
          [-100, 0, 100].forEach(black => {
            [0.5, 1, 2].forEach(channelGain => {
              const lut = buildToneLUT({ channelGain, exposure, skyPull, highlights, shadows, black });
              for (let i = 0; i < 255; i++) {
                expect(lut[i + 1]).toBeGreaterThanOrEqual(lut[i]);
              }
            });
          });
        });
      });
    });
  });
});

test('the black level moves the shadows and leaves white alone', () => {
  expect(buildToneLUT({ black: 100 })[255]).toBe(255);
  expect(buildToneLUT({ black: -100 })[255]).toBe(255);
  expect(buildToneLUT({ black: 100 })[40]).toBe(0);
  expect(buildToneLUT({ black: -100 })[0]).toBeGreaterThan(0);
});

test('raising exposure brightens without blowing out white or lifting black', () => {
  const brighter = buildToneLUT({ exposure: 1 });
  expect(brighter[128]).toBeGreaterThan(128);
  expect(brighter[255]).toBe(255);
  expect(brighter[0]).toBe(0);
  expect(buildToneLUT({ exposure: -1 })[128]).toBeLessThan(128);
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
    const lifted = buildToneLUT({ exposure: 1.5 });
    expect(lifted[190]).toBe(255);
    expect(lifted[255]).toBe(255);

    const recovered = buildToneLUT({ exposure: 1.5, highlights: -80 });
    expect(recovered[255]).toBeLessThan(255);
    expect(recovered[255]).toBeGreaterThan(recovered[190]);
    // And the bird, down in the midtones, keeps the exposure it was given.
    expect(recovered[90]).toBe(lifted[90]);
  });

  test('the pull leaves the shadows alone and scales what is bright', () => {
    // Below the knee — which opens at SKY_PIVOT * (1 - SKY_KNEE) — nothing moves.
    expect(applySkyPull(0.01, 2)).toBe(0.01);
    expect(applySkyPull(0, 2)).toBe(0);
    expect(applySkyPull(0.8, 0)).toBe(0.8);
    // Well above the knee it is exactly the scale it promises.
    expect(applySkyPull(0.9, 2)).toBeCloseTo(0.1762 + (0.9 - 0.1762) / 4, 12);
    expect(applySkyPull(0.9, 2)).toBeLessThan(applySkyPull(0.9, 1));

    let previous = -1;
    for (let l = 0; l <= 2; l += 0.002) {
      const got = applySkyPull(l, 2);
      expect(got).toBeGreaterThan(previous);
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

// The shadow lift is the other half: it brightens the bird and, unlike the sky
// pull's soft promise, it cannot reach the sky at all.
describe('the shadow lift', () => {
  test('it pins black and everything above the knee', () => {
    expect(applyShadows(0, 1)).toBe(0);
    [0.5, 0.8, 1.0, 1.4].forEach(v => expect(applyShadows(v, 1)).toBe(v));
    expect(applyShadows(0.2, 0)).toBe(0.2);

    // In between it brightens, and further the harder it is pushed.
    expect(applyShadows(0.2, 0.5)).toBeGreaterThan(0.2);
    expect(applyShadows(0.2, 1)).toBeGreaterThan(applyShadows(0.2, 0.5));

    // It meets the knee at slope 1, so there is no kink where it takes over.
    expect((0.5 - applyShadows(0.5 - 1e-7, 1)) / 1e-7).toBeCloseTo(1, 3);
  });

  test('it lifts the bird and leaves the sky bit for bit', () => {
    const plain = buildToneLUT({});
    const lifted = buildToneLUT({ shadows: 1 });
    // The values measured off the sample photo: bird's back 51, sky 249.
    expect(lifted[51]).toBeGreaterThan(plain[51] + 20);
    for (let i = 128; i < 256; i++) {
      expect(lifted[i]).toBe(plain[i]);
    }
  });

  test('the slider only ever lifts', () => {
    expect(shadowLift(0)).toBe(0);
    expect(shadowLift(-40)).toBe(0);
    expect(shadowLift(50)).toBe(0.5);
    expect(shadowLift(250)).toBe(1);
  });
});

describe('white balance', () => {
  test('temperature trades red against blue and tint moves green alone', () => {
    expect(whiteBalanceGains(0, 0)).toEqual({ r: 1, g: 1, b: 1 });

    const warm = whiteBalanceGains(100, 0);
    expect(warm.r).toBeGreaterThan(1);
    expect(warm.b).toBeLessThan(1);
    expect(warm.g).toBe(1);

    const cool = whiteBalanceGains(-100, 0);
    expect(cool.r).toBeCloseTo(warm.b, 12);
    expect(cool.b).toBeCloseTo(warm.r, 12);

    const magenta = whiteBalanceGains(0, 100);
    expect(magenta.g).toBeLessThan(1);
    expect(magenta.r).toBe(1);
    expect(magenta.b).toBe(1);
    expect(whiteBalanceGains(0, -100).g).toBeGreaterThan(1);

    // The sliders are clamped, not wrapped.
    expect(whiteBalanceGains(400, 0).r).toBe(whiteBalanceGains(100, 0).r);
  });

  test('the picker turns the colour it sampled neutral', () => {
    // The cyan cast measured off the sample photo's sky.
    const { temperature, tint } = greyPointWhiteBalance(237, 254, 255);
    expect(temperature).toBeGreaterThan(0); // cyan wants warming
    expect(Math.abs(temperature)).toBeLessThanOrEqual(100);
    expect(Math.abs(tint)).toBeLessThanOrEqual(100);

    // Push the sampled colour back through the curve the sliders describe.
    const gains = whiteBalanceGains(temperature, tint);
    const out = ['r', 'g', 'b'].map((ch, i) =>
      buildToneLUT({ channelGain: gains[ch] })[[237, 254, 255][i]]);
    expect(Math.max(...out) - Math.min(...out)).toBeLessThanOrEqual(1);
  });

  test('something already neutral asks for no correction', () => {
    expect(greyPointWhiteBalance(180, 180, 180)).toEqual({ temperature: 0, tint: 0 });
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
