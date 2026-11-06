/**
 * Best-effort notification sounds via the Web Audio API. Named sounds map to a
 * short synthesized tone, so no audio assets need bundling and the daemon can
 * "choose the sound" by name (issue #66). Everything is guarded: a missing
 * AudioContext (e.g. jsdom) or a browser autoplay block is swallowed silently —
 * a notification must never fail because its sound could not play.
 */

interface Tone {
  frequency: number;
  duration: number;
}

const TONES: Record<string, Tone | null> = {
  none: null,
  silent: null,
  default: { frequency: 880, duration: 0.15 },
  ping: { frequency: 880, duration: 0.12 },
  chime: { frequency: 1047, duration: 0.22 },
  glass: { frequency: 1319, duration: 0.18 },
  bell: { frequency: 660, duration: 0.25 },
};

function toneFor(name: string | undefined): Tone | null {
  const key = (name ?? 'default').toLowerCase();
  if (key in TONES) return TONES[key];
  return TONES.default;
}

let context: AudioContext | null = null;

function audioContext(): AudioContext | null {
  const Ctor =
    typeof window !== 'undefined'
      ? window.AudioContext ??
        (window as unknown as { webkitAudioContext?: typeof AudioContext })
          .webkitAudioContext
      : undefined;
  if (!Ctor) return null;
  if (!context) context = new Ctor();
  return context;
}

/** Plays the sound for the given name, if any. Silently no-ops on failure. */
export function playNotificationSound(name: string | undefined): void {
  const tone = toneFor(name);
  if (!tone) return;
  try {
    const ctx = audioContext();
    if (!ctx) return;
    // Autoplay policy may leave the context suspended until a user gesture.
    void ctx.resume?.();
    const oscillator = ctx.createOscillator();
    const gain = ctx.createGain();
    oscillator.type = 'sine';
    oscillator.frequency.value = tone.frequency;
    gain.gain.setValueAtTime(0.0001, ctx.currentTime);
    gain.gain.exponentialRampToValueAtTime(0.2, ctx.currentTime + 0.02);
    gain.gain.exponentialRampToValueAtTime(
      0.0001,
      ctx.currentTime + tone.duration,
    );
    oscillator.connect(gain);
    gain.connect(ctx.destination);
    oscillator.start();
    oscillator.stop(ctx.currentTime + tone.duration);
  } catch {
    // Sound is best-effort; ignore any failure.
  }
}
