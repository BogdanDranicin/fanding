import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Тесты гоняют настоящий модуль на подменённых глобальных объектах: проверять
// надо именно его логику включения тона, а не пересказ этой логики в моке.
// Ради этого — минимальные заглушки localStorage, document/window и WebAudio.

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
  state: 'suspended' | 'running' | 'closed' = 'suspended';
  destination = {};
  oscillators: FakeOscillator[] = [];
  resumeCalls = 0;
  private listeners: (() => void)[] = [];

  createOscillator() {
    const o = new FakeOscillator();
    this.oscillators.push(o);
    return o;
  }

  createGain() { return new FakeGain(); }

  addEventListener(_: string, fn: () => void) { this.listeners.push(fn); }

  resume() {
    this.resumeCalls++;
    return Promise.resolve();
  }

  /** Имитирует переход состояния, который делает сам браузер. */
  go(state: 'suspended' | 'running') {
    this.state = state;
    for (const fn of this.listeners) fn();
  }

  /** Живые (не остановленные) осцилляторы — это и есть удерживающий тон. */
  live() { return this.oscillators.filter((o) => o.started && !o.stopped); }
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

let mod: typeof import('./audioKeepAlive');

beforeEach(async () => {
  vi.stubGlobal('localStorage', fakeStorage());
  vi.stubGlobal('document', { addEventListener() {}, removeEventListener() {} });
  vi.stubGlobal('window', { addEventListener() {}, removeEventListener() {} });
  vi.resetModules();
  mod = await import('./audioKeepAlive');
});

afterEach(() => {
  mod.__resetKeepAliveForTests();
  vi.unstubAllGlobals();
});

function ctx(): FakeAudioContext {
  return new FakeAudioContext();
}

describe('удержание вкладки звуком', () => {
  it('не звучит, пока браузер не разрешил звук, и включается сам после разрешения', () => {
    const c = ctx();
    mod.keepAudioAlive(c as unknown as AudioContext, 'alarms');

    expect(c.live()).toHaveLength(0);
    expect(c.resumeCalls).toBeGreaterThan(0);

    // Пользователь щёлкнул по странице — браузер разрешил звук.
    c.go('running');
    expect(c.live()).toHaveLength(1);
  });

  it('держит один тон на любое число владельцев и гасит его на последнем', () => {
    const c = ctx();
    c.state = 'running';

    mod.keepAudioAlive(c as unknown as AudioContext, 'alarms');
    mod.keepAudioAlive(c as unknown as AudioContext, 'funding');
    expect(c.live()).toHaveLength(1);

    // Выключили сигналы по времени — уведомление о фандинге всё ещё ждёт.
    mod.releaseAudio('alarms');
    expect(c.live()).toHaveLength(1);

    mod.releaseAudio('funding');
    expect(c.live()).toHaveLength(0);
  });

  it('поднимает тон заново, когда браузер приостановил контекст', () => {
    const c = ctx();
    c.state = 'running';
    mod.keepAudioAlive(c as unknown as AudioContext, 'alarms');
    expect(c.live()).toHaveLength(1);

    // Ноутбук ушёл в сон: контекст приостановлен, тон вместе с ним.
    c.go('suspended');
    expect(c.live()).toHaveLength(0);
    expect(c.resumeCalls).toBeGreaterThan(0);

    // Машина проснулась.
    c.go('running');
    expect(c.live()).toHaveLength(1);
  });

  it('молчит, когда удержание выключено в настройках', () => {
    const c = ctx();
    c.state = 'running';
    mod.setKeepAliveEnabled(false);

    mod.keepAudioAlive(c as unknown as AudioContext, 'alarms');
    expect(c.live()).toHaveLength(0);

    // Вернули настройку — тон обязан появиться без перезагрузки страницы.
    mod.setKeepAliveEnabled(true);
    mod.refreshKeepAlive(c as unknown as AudioContext);
    expect(c.live()).toHaveLength(1);
  });

  it('рассказывает странице настроек, что происходит со звуком', () => {
    const c = ctx();
    expect(mod.audioStatus(null)).toMatchObject({ state: 'unavailable', blocked: true });
    expect(mod.audioStatus(c as unknown as AudioContext))
      .toMatchObject({ state: 'suspended', blocked: true, keepAlive: false });

    c.state = 'running';
    mod.keepAudioAlive(c as unknown as AudioContext, 'alarms');
    expect(mod.audioStatus(c as unknown as AudioContext))
      .toMatchObject({ state: 'running', blocked: false, keepAlive: true });
  });
});
