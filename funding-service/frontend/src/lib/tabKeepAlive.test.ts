import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

let audioContexts = 0;

class FakeAudioContext {
  state = 'running';
  destination = {};

  constructor() { audioContexts++; }
  createOscillator() { return { start() {}, stop() {}, connect: (n: unknown) => n, disconnect() {} }; }
  createGain() { return { gain: { value: 0 }, connect: (n: unknown) => n, disconnect() {} }; }
  addEventListener() {}
}

class FakeLocks {
  granted = 0;
  released = 0;

  request(_name: string, _opts: unknown, body: () => Promise<void>): Promise<void> {
    this.granted++;
    return body().then(() => { this.released++; });
  }

  get held() { return this.granted - this.released; }
}

function fakeStorage() {
  const map = new Map<string, string>();
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => void map.set(k, v),
    removeItem: (k: string) => void map.delete(k),
    clear: () => map.clear(),
    key: () => null,
    length: 0,
  } as unknown as Storage;
}

let mod: typeof import('./tabKeepAlive');
let locks: FakeLocks;
let storage: Storage;

async function load(withLocks = true) {
  locks = new FakeLocks();
  storage = fakeStorage();
  audioContexts = 0;
  vi.stubGlobal('AudioContext', FakeAudioContext);
  vi.stubGlobal('localStorage', storage);
  vi.stubGlobal('document', { addEventListener() {}, removeEventListener() {} });
  vi.stubGlobal('window', { addEventListener() {}, removeEventListener() {} });
  vi.stubGlobal('navigator', withLocks ? { locks } : {});
  vi.resetModules();
  mod = await import('./tabKeepAlive');
}

beforeEach(() => load());

afterEach(() => {
  mod.__resetKeepAliveForTests();
  vi.unstubAllGlobals();
});

describe('удержание вкладки', () => {
  it('держит вкладку локом и молча: звука на вкладке нет', async () => {
    mod.keepTabAlive('alarms');
    await Promise.resolve();

    expect(locks.held).toBe(1);
    expect(audioContexts).toBe(0);
    expect(mod.keepAliveStatus().mode).toBe('lock');
  });

  it('держит один лок на любое число владельцев и отпускает на последнем', async () => {
    mod.keepTabAlive('alarms');
    mod.keepTabAlive('funding');
    await Promise.resolve();
    expect(locks.granted).toBe(1);

    mod.releaseTabAlive('alarms');
    expect(locks.held).toBe(1);

    mod.releaseTabAlive('funding');
    await Promise.resolve();
    expect(locks.held).toBe(0);
  });

  it('без Web Locks вкладка остаётся без удержания, но звук не заводит', async () => {
    await load(false);
    mod.keepTabAlive('alarms');
    await Promise.resolve();

    expect(mod.keepAliveStatus().mode).toBe('none');
    expect(mod.keepAliveStatus().locks).toBe(false);
    expect(audioContexts).toBe(0);
  });

  it('заморозка вопреки локу только записывается, звук не включает', async () => {
    mod.keepTabAlive('alarms');
    await Promise.resolve();

    mod.__freezeForTests();
    await Promise.resolve();

    expect(mod.keepAliveStatus().frozeAt).not.toBeNull();
    expect(mod.keepAliveStatus().mode).toBe('lock');
    expect(locks.held).toBe(1);
    expect(audioContexts).toBe(0);
  });

  it('не считает отказом заморозку при уходе страницы в bfcache', async () => {
    mod.keepTabAlive('alarms');
    await Promise.resolve();

    mod.__leavingForTests();
    mod.__freezeForTests();
    await Promise.resolve();

    expect(mod.keepAliveStatus().frozeAt).toBeNull();
  });

  it('забывает старую настройку постоянного звука', async () => {
    locks = new FakeLocks();
    const s = fakeStorage();
    s.setItem('tab_keepalive_tone', '1');
    vi.stubGlobal('localStorage', s);
    vi.stubGlobal('document', { addEventListener() {}, removeEventListener() {} });
    vi.stubGlobal('window', { addEventListener() {}, removeEventListener() {} });
    vi.stubGlobal('navigator', { locks });
    vi.resetModules();
    mod = await import('./tabKeepAlive');

    mod.keepTabAlive('alarms');
    await Promise.resolve();

    expect(s.getItem('tab_keepalive_tone')).toBeNull();
    expect(mod.keepAliveStatus().mode).toBe('lock');
    expect(audioContexts).toBe(0);
  });

  it('забывает заморозку старше недели', async () => {
    storage.setItem('tab_keepalive_froze_at', String(Date.now() - 8 * 24 * 60 * 60 * 1000));
    mod.keepTabAlive('alarms');
    await Promise.resolve();

    expect(mod.keepAliveStatus().frozeAt).toBeNull();
  });
});
