const FROZE_KEY = 'tab_keepalive_froze_at';
const LEGACY_TONE_KEY = 'tab_keepalive_tone';

const FROZE_TTL_MS = 7 * 24 * 60 * 60 * 1000;

const LOCK_NAME = 'funding-tab-alive';

export type KeepAliveMode = 'lock' | 'none';

export interface KeepAliveStatus {
  wanted: boolean;
  mode: KeepAliveMode;
  frozeAt: number | null;
  locks: boolean;
}

const owners = new Set<string>();

let releaseLock: (() => void) | null = null;
let lockPending = false;
let bound = false;
let leaving = false;

function locksAvailable(): boolean {
  return typeof navigator !== 'undefined' && navigator.locks != null;
}

function dropLegacyToneSetting(): void {
  try {
    localStorage.removeItem(LEGACY_TONE_KEY);
  } catch {
    // no-op
  }
}

dropLegacyToneSetting();

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
  return {
    wanted: owners.size > 0,
    mode: releaseLock ? 'lock' : 'none',
    frozeAt: frozeAt(),
    locks: locksAvailable(),
  };
}

function apply(): void {
  if (owners.size === 0) {
    dropLock();
    return;
  }
  takeLock();
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
