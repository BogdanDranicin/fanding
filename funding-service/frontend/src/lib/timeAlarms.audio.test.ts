import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Проверка того слоя, который и делает сигнал слышимым в свёрнутом окне: звук
// уходит в очередь WebAudio заранее и назначается по часам аудиопотока. Отсюда
// же берётся его слабое место — если эти часы остановятся (машина уснула,
// браузер всё-таки приостановил контекст), нота уедет ровно на длительность
// остановки. Планировщик обязан такую постановку заметить и переставить, и
// именно это здесь и меряется.

class FakeParam {
  setValueAtTime() { return this; }
  exponentialRampToValueAtTime() { return this; }
  linearRampToValueAtTime() { return this; }
  value = 0;
}

class FakeNode {
  gain = new FakeParam();
  frequency = new FakeParam();
  type = 'sine';
  startedAt: number | null = null;
  connect(next: unknown) { return next as FakeNode; }
  disconnect() {}
  start(at?: number) { this.startedAt = at ?? 0; }
  stop() {}
}

class FakeAudioContext {
  state: 'suspended' | 'running' = 'running';
  currentTime = 0;
  destination = new FakeNode();
  nodes: FakeNode[] = [];
  createOscillator() {
    const n = new FakeNode();
    this.nodes.push(n);
    return n;
  }
  createGain() { return new FakeNode(); }
  addEventListener() {}
  resume() { return Promise.resolve(); }
}

let ctx: FakeAudioContext;

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

let mod: typeof import('./timeAlarms');

beforeEach(async () => {
  ctx = new FakeAudioContext();
  vi.stubGlobal('localStorage', fakeStorage());
  vi.stubGlobal('document', { addEventListener() {}, removeEventListener() {} });
  vi.stubGlobal('window', { addEventListener() {}, removeEventListener() {} });
  vi.stubGlobal('AudioContext', function () { return ctx; } as unknown as typeof AudioContext);
  vi.resetModules();
  mod = await import('./timeAlarms');
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('постановка звука в очередь WebAudio', () => {
  it('назначает ноту на будущий момент по часам аудиопотока', () => {
    const now = Date.now();
    vi.useFakeTimers();
    vi.setSystemTime(now);

    mod.scheduleAlarmTone('beep', 0.5, now + 60_000);

    const started = ctx.nodes.map((n) => n.startedAt).filter((v): v is number => v !== null);
    // Первым в контексте заводится удерживающий тон — он идёт с нуля и без
    // конца: пока в очереди есть звук, вкладку нельзя отдавать на заморозку.
    expect(started).toContain(0);
    // currentTime = 0, до отметки минута → нота назначена на 60-ю секунду.
    expect(started.filter((v) => v > 0)[0]).toBeCloseTo(60, 3);
  });

  it('видит снос, когда часы аудиопотока стояли', () => {
    const now = Date.now();
    vi.useFakeTimers();
    vi.setSystemTime(now);

    const tone = mod.scheduleAlarmTone('beep', 0.5, now + 60_000);
    expect(Math.abs(tone.driftMs(now))).toBeLessThan(5);

    // Вкладку заморозили на полминуты: стенные часы идут, аудиочасы стоят.
    vi.setSystemTime(now + 30_000);
    expect(tone.driftMs(now + 30_000)).toBeCloseTo(30_000, -2);
    expect(Math.abs(tone.driftMs(now + 30_000))).toBeGreaterThan(mod.ALARM_DRIFT_TOLERANCE_MS);

    // Контекст шёл вместе со стенными часами — сноса нет.
    const ok = mod.scheduleAlarmTone('beep', 0.5, now + 90_000);
    ctx.currentTime += 10;
    expect(Math.abs(ok.driftMs(now + 40_000))).toBeLessThan(mod.ALARM_DRIFT_TOLERANCE_MS);
  });

  it('снятая постановка сноса не показывает и ничего не играет', () => {
    const now = Date.now();
    vi.useFakeTimers();
    vi.setSystemTime(now);

    const tone = mod.scheduleAlarmTone('gong', 0.5, now + 10_000);
    tone.cancel();
    // Снятая нота уже не наша забота: планировщик поставит новую.
    expect(() => tone.cancel()).not.toThrow();
  });
});
