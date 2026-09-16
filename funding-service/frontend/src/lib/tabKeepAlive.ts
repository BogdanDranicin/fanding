import { alertAudioContext } from './alertSound';

const TONE_KEY = 'tab_keepalive_tone';
const FROZE_KEY = 'tab_keepalive_froze_at';

const FROZE_TTL_MS = 7 * 24 * 60 * 60 * 1000;

const LOCK_NAME = 'funding-tab-alive';

const TONE_HZ = 30;
const TONE_GAIN = 0.0025;

const HEARTBEAT_MS = 30 * 1000;

export type KeepAliveMode = 'tone' | 'lock' | 'none';

export interface KeepAliveStatus {
  wanted: boolean;
  mode: KeepAliveMode;
  toneEnabled: boolean;
  frozeAt: number | null;
  locks: boolean;
  audio: AudioContextState | 'unavailable';
  blocked: boolean;
}

interface Tone {
  ctx: AudioContext;
  osc: OscillatorNode;
  gain: GainNode;
}

const owners = new Set<string>();

let releaseLock: (() => void) | null = null;
let lockPending = false;
let tone: Tone | null = null;
let bound = false;
let leaving = false;
let heartbeat: ReturnType<typeof setInterval> | null = null;

function locksAvailable(): boolean {
  return typeof navigator !== 'undefined' && navigator.locks != null;
}

export function isToneEnabled(): boolean {
  try {
    return localStorage.getItem(TONE_KEY) !== '0';
  } catch {
    return true;
  }
}

export function setToneEnabled(on: boolean): void {
  try {
    localStorage.setItem(TONE_KEY, on ? '1' : '0');
  } catch {
    // no-op
  }
  apply();
}

function frozeAt(): number | null {
  try {
    const v = Number(localStorage.getItem(FROZE_KEY));
    return Number.isFinite(v) && v > 0 && Date.now() - v < FROZE_TTL_MS ? v : null;
  } catch {
    return null;
  }
}

export function keepTabAlive(owner = 'default'): void {
  owners.add(owner);
  bind();
  apply();
}

export function releaseTabAlive(owner = 'default'): void {
  owners.delete(owner);
  apply();
}

export function keepAliveStatus(): KeepAliveStatus {
  const c = tone?.ctx ?? null;
  return {
    wanted: owners.size > 0,
    mode: tone ? 'tone' : releaseLock ? 'lock' : 'none',
    toneEnabled: isToneEnabled(),
    frozeAt: frozeAt(),
    locks: locksAvailable(),
    audio: c ? c.state : 'unavailable',
    blocked: isToneEnabled() && owners.size > 0 && tone === null,
  };
}

function apply(): void {
  if (owners.size === 0) {
    dropLock();
    stopTone();
    stopHeartbeat();
    return;
  }
  takeLock();
  if (isToneEnabled()) startTone();
  else stopTone();
  startHeartbeat();
}

function takeLock(): void {
  if (lockPending || releaseLock || !locksAvailable()) return;
  lockPending = true;
  void navigator.locks
    .request(LOCK_NAME, { mode: 'shared' }, () =>
      new Promise<void>((done) => {
        lockPending = false;
        releaseLock = () => {
          releaseLock = null;
          done();
        };
        if (owners.size === 0) releaseLock();
      }),
    )
    .catch(() => {
      lockPending = false;
      releaseLock = null;
    });
}

function dropLock(): void {
  releaseLock?.();
}

function startTone(): void {
  if (tone && tone.ctx.state === 'running') return;
  if (tone) stopTone();
  const c = alertAudioContext();
  if (!c) return;
  if (c.state !== 'running') {
    void c
      .resume()
      .then(() => {
        if (owners.size > 0 && isToneEnabled()) startTone();
      })
      .catch(() => {});
    return;
  }
  try {
    const osc = c.createOscillator();
    const gain = c.createGain();
    osc.type = 'sine';
    osc.frequency.value = TONE_HZ;
    gain.gain.value = TONE_GAIN;
    osc.connect(gain).connect(c.destination);
    osc.start();
    tone = { ctx: c, osc, gain };
  } catch {
    // no-op
  }
}

function stopTone(): void {
  if (!tone) return;
  try {
    tone.osc.stop();
    tone.osc.disconnect();
    tone.gain.disconnect();
  } catch {
    // no-op
  }
  tone = null;
}

function startHeartbeat(): void {
  if (heartbeat !== null || typeof setInterval !== 'function') return;
  heartbeat = setInterval(() => {
    if (owners.size === 0) {
      stopHeartbeat();
      return;
    }
    takeLock();
    if (isToneEnabled()) startTone();
  }, HEARTBEAT_MS);
}

function stopHeartbeat(): void {
  if (heartbeat === null) return;
  clearInterval(heartbeat);
  heartbeat = null;
}

function onFreeze(): void {
  if (leaving || owners.size === 0) return;
  try {
    localStorage.setItem(FROZE_KEY, String(Date.now()));
  } catch {
    // no-op
  }
}

function revive(): void {
  if (owners.size === 0) return;
  apply();
}

function bind(): void {
  if (bound || typeof document === 'undefined') return;
  bound = true;

  document.addEventListener('freeze', onFreeze);
  document.addEventListener('resume', revive);
  document.addEventListener('visibilitychange', revive);
  window.addEventListener('focus', revive);
  window.addEventListener('pointerdown', revive);
  window.addEventListener('keydown', revive);
  window.addEventListener('touchstart', revive);
  window.addEventListener('pageshow', () => {
    leaving = false;
    revive();
  });
  window.addEventListener('pagehide', () => {
    leaving = true;
  });
}

export function __resetKeepAliveForTests(): void {
  dropLock();
  stopTone();
  stopHeartbeat();
  owners.clear();
  lockPending = false;
  bound = false;
  leaving = false;
}

export function __freezeForTests(): void {
  onFreeze();
  revive();
}

export function __leavingForTests(): void {
  leaving = true;
}

export function __reviveForTests(): void {
  revive();
}
