import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Тесты гоняют настоящий модуль на подменённых глобальных объектах: проверять
// надо именно его логику выбора удержания, а не пересказ этой логики в моке.
// Ради этого — минимальные заглушки localStorage, document/window, LockManager
// и WebAudio.

class FakeOscillator {
  type = 'sine';
  frequency = { value: 0 };
  started = false;
  stopped = false;
  connect(next: unknown) { return next as FakeGain; }
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

  /** Живые (не остановленные) осцилляторы — это и есть запасной тон. */
  live() { return this.oscillators.filter((o) => o.started && !o.stopped); }
}

/**
 * Заглушка Web Locks. Держит счётчик выданных и отпущенных локов: именно по
 * нему видно, что вкладка удерживается тихо, — снаружи у лока нет никаких
 * признаков вроде значка на вкладке.
 */
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

async function load(withLocks = true) {
  locks = new FakeLocks();
  vi.stubGlobal('localStorage', fakeStorage());
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

function ctx(): FakeAudioContext {
  return new FakeAudioContext();
}

describe('удержание вкладки', () => {
  it('держит вкладку локом и молча: звука на вкладке нет', async () => {
    const c = ctx();
    mod.keepTabAlive(c as unknown as AudioContext, 'alarms');
    await Promise.resolve();

    expect(locks.held).toBe(1);
    expect(c.live()).toHaveLength(0);
    expect(mod.keepAliveStatus(c as unknown as AudioContext).mode).toBe('lock');
  });

  it('держит один лок на любое число владельцев и отпускает на последнем', async () => {
    const c = ctx();
    mod.keepTabAlive(c as unknown as AudioContext, 'alarms');
    mod.keepTabAlive(c as unknown as AudioContext, 'funding');
    await Promise.resolve();
    expect(locks.granted).toBe(1);

    // Выключили сигналы по времени — уведомление о фандинге всё ещё ждёт.
    mod.releaseTabAlive('alarms');
    expect(locks.held).toBe(1);

    mod.releaseTabAlive('funding');
    await Promise.resolve();
    expect(locks.held).toBe(0);
  });

  it('без Web Locks сразу берётся за запасной тон', async () => {
    await load(false);
    const c = ctx();
    mod.keepTabAlive(c as unknown as AudioContext, 'alarms');

    expect(c.live()).toHaveLength(1);
    expect(mod.keepAliveStatus(c as unknown as AudioContext).mode).toBe('tone');
  });

  it('включает тон, если браузер заморозил вкладку вопреки локу', async () => {
    const c = ctx();
    mod.keepTabAlive(c as unknown as AudioContext, 'alarms');
    await Promise.resolve();
    expect(c.live()).toHaveLength(0);

    // Браузер сообщил о заморозке и вернул страницу к жизни.
    mod.__freezeForTests();
    await Promise.resolve();

    expect(c.live()).toHaveLength(1);
    expect(mod.keepAliveStatus(c as unknown as AudioContext).frozeAt).not.toBeNull();
  });

  it('не считает отказом заморозку при уходе страницы в bfcache', async () => {
    const c = ctx();
    mod.keepTabAlive(c as unknown as AudioContext, 'alarms');
    await Promise.resolve();

    mod.__leavingForTests();
    mod.__freezeForTests();
    await Promise.resolve();

    expect(c.live()).toHaveLength(0);
    expect(mod.keepAliveStatus(c as unknown as AudioContext).frozeAt).toBeNull();
  });

  it('слушается настройки: тон включается и снимается без перезагрузки', async () => {
    const c = ctx();
    mod.keepTabAlive(c as unknown as AudioContext, 'alarms');
    await Promise.resolve();
    expect(c.live()).toHaveLength(0);

    mod.setToneForced(true);
    expect(c.live()).toHaveLength(1);
    // Лок при этом никуда не девается: тон — добавка к нему, а не замена.
    expect(locks.held).toBe(1);

    mod.setToneForced(false);
    expect(c.live()).toHaveLength(0);
  });

  it('не звучит, пока браузер не разрешил звук, и включается сам после разрешения', async () => {
    await load(false);
    const c = ctx();
    c.state = 'suspended';
    mod.keepTabAlive(c as unknown as AudioContext, 'alarms');

    expect(c.live()).toHaveLength(0);
    expect(c.resumeCalls).toBeGreaterThan(0);

    // Пользователь щёлкнул по странице — браузер разрешил звук.
    mod.__reviveForTests();
    expect(c.live()).toHaveLength(1);
  });
});
