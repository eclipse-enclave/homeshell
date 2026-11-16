// The virtual desktop for the README demo: a HomeShell window in Chromium, the real X pointer
// moved and clicked with xdotool (so it shows in the recording and crosses windows), and ffmpeg
// capturing the X screen. Expects DISPLAY to be the demo's Xvfb (xsession.sh).
import { execFileSync, spawn } from 'node:child_process';
import { createRequire } from 'node:module';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const require = createRequire(join(here, '../../webapp/apps/web-e2e/package.json'));
const { chromium } = require('@playwright/test');

export const SCREEN = { width: 1280, height: 800 };
export const pause = (ms) => new Promise((r) => setTimeout(r, ms));
const xdotool = (...args) => execFileSync('xdotool', args.map(String), { encoding: 'utf8' }).trim();

// -- Browser ------------------------------------------------------------------------------------

/** HomeShell in a Chromium app window covering the screen, signed in with the token cookie. */
export async function openBrowser(baseUrl, token) {
  const context = await chromium.launchPersistentContext(mkdtempSync(join(tmpdir(), 'hs-demo-chromium-')), {
    headless: false,
    viewport: null,
    colorScheme: 'dark',
    ignoreDefaultArgs: ['--enable-automation'],
    // X11 explicitly: with WAYLAND_DISPLAY set Chromium would open on the user's own desktop.
    env: { ...process.env, WAYLAND_DISPLAY: '' },
    args: [
      `--app=${baseUrl}`,
      '--ozone-platform=x11',
      '--window-position=0,0',
      `--window-size=${SCREEN.width},${SCREEN.height}`,
    ],
  });
  await context.addCookies([{ name: 'hs_api_token', value: token, url: baseUrl }]);
  const page = context.pages()[0] ?? (await context.waitForEvent('page'));
  await page.goto(baseUrl);
  return { context, page };
}

/** Where the page's viewport starts on the screen. */
async function viewportOrigin(page) {
  return page.evaluate(() => ({
    x: window.screenX + (window.outerWidth - window.innerWidth) / 2,
    y: window.screenY + window.outerHeight - window.innerHeight,
  }));
}

// -- Pointer and keyboard ----------------------------------------------------------------------

let pointer = { x: SCREEN.width / 2, y: SCREEN.height / 2 };

/** Glides the X pointer to a screen point, easing in and out, about 900 px per second. */
export async function glide(x, y) {
  const from = pointer;
  const distance = Math.hypot(x - from.x, y - from.y);
  const steps = Math.max(8, Math.round(distance / 22));
  for (let i = 1; i <= steps; i++) {
    const t = i / steps;
    const e = t < 0.5 ? 2 * t * t : 1 - (-2 * t + 2) ** 2 / 2;
    xdotool('mousemove', Math.round(from.x + (x - from.x) * e), Math.round(from.y + (y - from.y) * e));
    await pause(16);
  }
  pointer = { x, y };
}

/** Screen point at the centre of `locator`, or at `offset` from its top left. */
export async function screenPoint(page, locator, offset) {
  await locator.waitFor({ state: 'visible' });
  await locator.scrollIntoViewIfNeeded();
  const box = await locator.boundingBox();
  const origin = await viewportOrigin(page);
  return {
    x: Math.round(origin.x + box.x + (offset ? offset.x : box.width / 2)),
    y: Math.round(origin.y + box.y + (offset ? offset.y : box.height / 2)),
  };
}

export async function pointTo(page, locator, offset) {
  const p = await screenPoint(page, locator, offset);
  await glide(p.x, p.y);
}

export async function clickAt(x, y) {
  await glide(x, y);
  await pause(220);
  xdotool('click', 1);
  await pause(120);
}

export async function click(page, locator, offset) {
  const p = await screenPoint(page, locator, offset);
  await clickAt(p.x, p.y);
}

/** Types into whatever has the keyboard focus, like a person (xdotool type). */
export async function typeText(text, delayMs = 45) {
  xdotool('type', '--delay', delayMs, text);
}

export const key = (...names) => xdotool('key', ...names);

// -- Windows -----------------------------------------------------------------------------------

/** A window's content area on screen (xwininfo; xdotool's geometry is off by the frame). */
function geometry(id) {
  const info = execFileSync('xwininfo', ['-id', id], { encoding: 'utf8' });
  const num = (label) => Number(info.match(new RegExp(`${label}:\\s+(-?\\d+)`))[1]);
  return { id, x: num('Absolute upper-left X'), y: num('Absolute upper-left Y'), width: num('Width'), height: num('Height') };
}

/** Every window id currently mapped, to tell a new one apart. */
export function windowIds() {
  try {
    return new Set(xdotool('search', '--onlyvisible', '--name', '.').split('\n').filter(Boolean));
  } catch {
    return new Set(); // xdotool exits 1 when nothing matches
  }
}

/** The first sizeable window mapped after `before` (windowIds()), once it has stopped moving. */
export async function waitNewWindow(before, timeoutMs = 30_000) {
  const end = Date.now() + timeoutMs;
  let last;
  for (;;) {
    const id = [...windowIds()].find((w) => !before.has(w) && geometry(w).width > 200);
    if (id) {
      const geo = geometry(id);
      if (last && JSON.stringify(last) === JSON.stringify(geo)) return geo;
      last = geo;
    }
    if (Date.now() > end) throw new Error('no new window opened');
    await pause(250);
  }
}

/** Waits until window `win` is gone. */
export async function waitGone(win, timeoutMs = 10_000) {
  const end = Date.now() + timeoutMs;
  while (windowIds().has(win.id)) {
    if (Date.now() > end) throw new Error(`window ${win.id} did not close`);
    await pause(150);
  }
}

/** Clicks the × of a window's title bar (metacity's frame, above the content area). */
export async function closeWindow(win) {
  await clickAt(win.x + win.width - 19, win.y - 20);
  await waitGone(win);
}

// -- Capture -----------------------------------------------------------------------------------

/** Records the X screen to `file` until the returned stop() is awaited. */
export function startCapture(file, framerate = 25) {
  const ff = spawn(
    process.env.FFMPEG ?? 'ffmpeg',
    ['-v', 'error', '-y', '-f', 'x11grab', '-draw_mouse', '1', '-framerate', String(framerate),
      '-video_size', `${SCREEN.width}x${SCREEN.height}`, '-i', process.env.DISPLAY,
      '-c:v', 'libx264', '-preset', 'ultrafast', '-crf', '0', '-pix_fmt', 'yuv444p', file],
    { stdio: ['pipe', 'inherit', 'inherit'] },
  );
  const started = Date.now();
  const timeline = [{ t: 0, mode: 'normal', label: 'start' }];
  return {
    /** From now on the video plays as `mode`: normal, fast (sped up) or cut (dropped). */
    mark(mode, label = '') {
      timeline.push({ t: (Date.now() - started) / 1000, mode, label });
    },
    async stop(timelineFile) {
      timeline.push({ t: (Date.now() - started) / 1000, mode: 'end', label: 'end' });
      ff.stdin.write('q');
      await new Promise((r) => ff.on('exit', r));
      writeFileSync(timelineFile, JSON.stringify(timeline, null, 1));
    },
  };
}
