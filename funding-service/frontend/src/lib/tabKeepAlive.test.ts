import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

class FakeOscillator {
  type = 'sine';
  frequency = { value: 0 };
  started = false;
  stopped = false;
  connect(next: unknown) { return next; }
  disconnect() {}
  start() { this.started = true; }
  stop() { this.stopped = true; }
}

class FakeGain {
  gain = { value: 0 };
  connect(next: unknown) { return next; }
  disconnect() {}
}

class FakeAudioContext {
  state: 'suspended' | 'running' | 'closed' = 'running';
  destination = {};
  oscillators: FakeOscillator[] = [];
  resumeCalls = 0;

  constructor() { audio = this as unknown as FakeAudioContext; }

  createOscillator() {
    const o = new FakeOscillator();
    this.oscillators.push(o);
    return o;
  }

  createGain() { return new FakeGain(); }
  addEventListener() {}

  resume() {
    this.resumeCalls++;
    this.state = 'running';
    return Promise.resolve();
  }

  live() { return this.oscillators.filter((o) => o.started && !o.stopped); }
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
let audio: FakeAudioContext | null = null;

async function load(withLocks = true, prepare?: (s: Storage) => void) {
  locks = new FakeLocks();
  storage = fakeStorage();
  audio = null;
  prepare?.(storage);
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
  it('держит и локом, и неслышимым тоном: одного лока для долго скрытой вкладки мало', async () => {
    mod.keepTabAlive('alarms');
    await Promise.resolve();

    expect(locks.held).toBe(1);
    expect(audio!.live()).toHaveLength(1);
    expect(audio!.live()[0].frequency.value).toBe(30);
    expect(mod.keepAliveStatus().mode).toBe('tone');
  });

  it('держит один лок и один тон на любое число владельцев, снимает на последнем', async () => {
    mod.keepTabAlive('alarms');
    mod.keepTabAlive('funding');
    await Promise.resolve();
    expect(locks.granted).toBe(1);
    expect(audio!.live()).toHaveLength(1);

    mod.releaseTabAlive('alarms');
    expect(locks.held).toBe(1);
    expect(audio!.live()).toHaveLength(1);

    mod.releaseTabAlive('funding');
    await Promise.resolve();
    expect(locks.held).toBe(0);
    expect(audio!.live()).toHaveLength(0);
    expect(mod.keepAliveStatus().mode).toBe('none');
  });

  it('без Web Locks вкладку держит один тон', async () => {
    await load(false);
    mod.keepTabAlive('alarms');
    await Promise.resolve();

    expect(mod.keepAliveStatus().locks).toBe(false);
    expect(mod.keepAliveStatus().mode).toBe('tone');
    expect(audio!.live()).toHaveLength(1);
  });

  it('выключенный в настройках тон не заводится и снимается на лету', async () => {
    await load(true, (s) => s.setItem('tab_keepalive_tone', '0'));
    mod.keepTabAlive('alarms');
    await Promise.resolve();

    expect(mod.keepAliveStatus().mode).toBe('lock');
    expect(audio).toBeNull();

    mod.setToneEnabled(true);
    expect(audio!.live()).toHaveLength(1);

    mod.setToneEnabled(false);
    expect(audio!.live()).toHaveLength(0);
    expect(storage.getItem('tab_keepalive_tone')).toBe('0');
  });

  it('ждёт разрешения на звук и заводит тон, когда контекст запустился', async () => {
    const started = FakeAudioContext.prototype.resume;
    FakeAudioContext.prototype.resume = function () {
      this.resumeCalls++;
      this.state = 'running';
      return Promise.resolve();
    };
    const suspended = class extends FakeAudioContext {
      constructor() { super(); this.state = 'suspended'; }
    };
    vi.stubGlobal('AudioContext', suspended);

    mod.keepTabAlive('alarms');
    expect(audio!.live()).toHaveLength(0);

    await Promise.resolve();
    await Promise.resolve();

    expect(audio!.resumeCalls).toBeGreaterThan(0);
    expect(audio!.live()).toHaveLength(1);
    FakeAudioContext.prototype.resume = started;
  });

  it('поднимает упавший тон на следующем пробуждении вкладки', async () => {
    mod.keepTabAlive('alarms');
    await Promise.resolve();
    const first = audio!.live()[0];

    audio!.state = 'suspended';
    mod.__reviveForTests();
    await Promise.resolve();
    await Promise.resolve();

    expect(first.stopped).toBe(true);
    expect(audio!.live()).toHaveLength(1);
    expect(audio!.live()[0]).not.toBe(first);
  });

  it('записывает заморозку вопреки удержанию', async () => {
    mod.keepTabAlive('alarms');
    await Promise.resolve();

    mod.__freezeForTests();
    await Promise.resolve();

    expect(mod.keepAliveStatus().frozeAt).not.toBeNull();
    expect(locks.held).toBe(1);
    expect(audio!.live()).toHaveLength(1);
  });

  it('не считает отказом заморозку при уходе страницы в bfcache', async () => {
    mod.keepTabAlive('alarms');
    await Promise.resolve();

    mod.__leavingForTests();
    mod.__freezeForTests();
    await Promise.resolve();

    expect(mod.keepAliveStatus().frozeAt).toBeNull();
  });

  it('забывает заморозку старше недели', async () => {
    storage.setItem('tab_keepalive_froze_at', String(Date.now() - 8 * 24 * 60 * 60 * 1000));
    mod.keepTabAlive('alarms');
    await Promise.resolve();

    expect(mod.keepAliveStatus().frozeAt).toBeNull();
  });
});
