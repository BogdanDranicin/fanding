// Сигналы по времени: переход часа, получаса, произвольная отметка и любой шаг
// между ними. Живёт целиком в браузере — расписание не про рынок, а про самого
// пользователя, и серверу о нём знать незачем.
//
// Всё время считается по Москве. Москва не переводит часы, поэтому смещение
// ровно +3 и его можно держать числом: Intl понадобился бы только ради пояса
// с переходами, зато принёс бы в арифметику расписания разбор строк.

import { alertAudioContext, getCustomSoundDataURL } from './alertSound';

const ALARMS_KEY = 'time_alarms.v1';
const ENABLED_KEY = 'time_alarms_enabled';
const VOLUME_KEY = 'time_alarms_volume';

const MIN = 60_000;
const DAY = 24 * 60 * MIN;
/** Смещение Москвы от UTC в миллисекундах. */
const MSK = 3 * 60 * MIN;

/** Тон сигнала. `custom` — файл, загруженный в настройках звука фандинга. */
export type AlarmTone = 'chime' | 'beep' | 'double' | 'gong' | 'custom';

export const TONES: { value: AlarmTone; label: string }[] = [
  { value: 'chime', label: 'чайм' },
  { value: 'beep', label: 'бип' },
  { value: 'double', label: 'двойной бип' },
  { value: 'gong', label: 'гонг' },
  { value: 'custom', label: 'свой файл' },
];

export interface TimeAlarm {
  id: string;
  enabled: boolean;
  /** `interval` — раз в N минут, `at` — в конкретное время. */
  kind: 'interval' | 'at';
  /** Шаг для `interval`, минуты: 60 — раз в час, 30 — на переход получаса. */
  everyMin: number;
  /**
   * Первая отметка дня для `interval`, минуты от полуночи МСК. Ноль — ровно
   * в :00. Отсчёт начинается заново каждый день, а не идёт сплошной лентой от
   * начала времён: «каждые два часа начиная с 09:00» должно означать одно и то
   * же и сегодня, и завтра, даже если шаг не делит сутки нацело.
   */
  startMin: number;
  /** Отметка для `at`, минуты от полуночи МСК. */
  atMin: number;
  /** Не звонить в субботу и воскресенье. */
  weekdaysOnly: boolean;
  /** За сколько секунд до отметки звучит сигнал. Ноль — точно в момент. */
  leadSec: number;
  tone: AlarmTone;
  /** Своё название. Пустое — подпись собирается из расписания. */
  label: string;
}

/** Начало московских суток, в которые попал момент ms. */
export function mskDayStart(ms: number): number {
  return Math.floor((ms + MSK) / DAY) * DAY - MSK;
}

/** День недели по Москве: 0 — воскресенье. */
export function mskWeekday(ms: number): number {
  // 1 января 1970 — четверг, поэтому к номеру суток прибавляется 4.
  return (Math.floor((ms + MSK) / DAY) + 4) % 7;
}

function isWeekend(ms: number): boolean {
  const d = mskWeekday(ms);
  return d === 0 || d === 6;
}

/** «ЧЧ:ММ» из минут от полуночи. */
export function clockOf(minutes: number): string {
  const m = ((Math.round(minutes) % 1440) + 1440) % 1440;
  return `${String(Math.floor(m / 60)).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`;
}

/** Минуты от полуночи из «ЧЧ:ММ». Непонятное время даёт null. */
export function minutesOf(clock: string): number | null {
  const m = /^(\d{1,2}):(\d{2})$/.exec(clock.trim());
  if (!m) return null;
  const hh = Number(m[1]);
  const mm = Number(m[2]);
  if (hh > 23 || mm > 59) return null;
  return hh * 60 + mm;
}

/**
 * Ближайшая отметка расписания строго позже `after`. Ноль означает, что отметок
 * нет вовсе — например, у сигнала со сломанным шагом.
 */
export function nextOccurrence(a: TimeAlarm, after: number): number {
  const day0 = mskDayStart(after);
  // Двух недель хватает с запасом: пропуска длиннее выходных не бывает.
  for (let i = 0; i < 14; i++) {
    const dayStart = day0 + i * DAY;
    if (a.weekdaysOnly && isWeekend(dayStart)) continue;

    if (a.kind === 'at') {
      const t = dayStart + a.atMin * MIN;
      if (t > after) return t;
      continue;
    }

    const step = a.everyMin * MIN;
    if (!Number.isFinite(step) || step <= 0) return 0;
    let t = dayStart + a.startMin * MIN;
    if (t <= after) t += (Math.floor((after - t) / step) + 1) * step;
    // Отметка, уехавшая за полночь, принадлежит уже следующему дню — её выдаст
    // следующий виток, отсчитав от собственной первой отметки того дня.
    if (t > after && t < dayStart + DAY) return t;
  }
  return 0;
}

/**
 * Когда должен прозвучать сигнал: отметка минус предупреждение. Строго позже
 * `after`, чтобы пересчёт сразу после срабатывания не возвращал тот же момент.
 */
export function nextFireAt(a: TimeAlarm, after: number): number {
  const lead = Math.max(0, a.leadSec) * 1000;
  const occ = nextOccurrence(a, after + lead);
  return occ ? occ - lead : 0;
}

const WORD_MIN = ['минут', 'минуту', 'минуты'];
const WORD_HOUR = ['часов', 'час', 'часа'];

// plural — русская форма числительного: 21 минуту, 22 минуты, 25 минут.
function plural(n: number, forms: string[]): string {
  const mod100 = n % 100;
  const mod10 = n % 10;
  if (mod100 >= 11 && mod100 <= 14) return forms[0];
  if (mod10 === 1) return forms[1];
  if (mod10 >= 2 && mod10 <= 4) return forms[2];
  return forms[0];
}

/** Шаг словами: «час», «2 часа», «30 минут». */
export function stepLabel(everyMin: number): string {
  if (everyMin % 60 === 0) {
    const h = everyMin / 60;
    return h === 1 ? 'час' : `${h} ${plural(h, WORD_HOUR)}`;
  }
  return `${everyMin} ${plural(everyMin, WORD_MIN)}`;
}

// MARKS_SHOWN — сколько отметок внутри часа ещё имеет смысл перечислять подряд.
const MARKS_SHOWN = 4;

/** Подпись расписания — то, что читает пользователь в списке сигналов. */
export function describeAlarm(a: TimeAlarm): string {
  let s: string;
  if (a.kind === 'at') {
    s = `в ${clockOf(a.atMin)}`;
  } else if (a.everyMin === 1) {
    s = 'каждую минуту';
  } else if (a.everyMin === 60) {
    s = `каждый час в :${String(a.startMin % 60).padStart(2, '0')}`;
  } else if (a.everyMin < 60 && 60 % a.everyMin === 0 && a.startMin < a.everyMin
    && 60 / a.everyMin <= MARKS_SHOWN) {
    // Отметки внутри часа перечисляем, только пока их несколько: список из
    // шестидесяти значений занимает полстраницы и ничего не объясняет.
    const marks: string[] = [];
    for (let m = a.startMin; m < 60; m += a.everyMin) marks.push(`:${String(m).padStart(2, '0')}`);
    s = `каждые ${stepLabel(a.everyMin)} — ${marks.join(', ')}`;
  } else {
    s = `каждые ${stepLabel(a.everyMin)}`;
    if (a.startMin > 0) s += `, начиная с ${clockOf(a.startMin)}`;
  }
  if (a.leadSec > 0) s += `, сигнал за ${a.leadSec} с`;
  if (a.weekdaysOnly) s += ', по будням';
  return s;
}

/** Название сигнала: своё, если задано, иначе расписание словами. */
export function alarmTitle(a: TimeAlarm): string {
  return a.label.trim() || describeAlarm(a);
}

// ── Хранение ────────────────────────────────────────────────────────────────

export function isAlarmsEnabled(): boolean {
  return localStorage.getItem(ENABLED_KEY) !== '0';
}

export function setAlarmsEnabled(on: boolean): void {
  localStorage.setItem(ENABLED_KEY, on ? '1' : '0');
}

export function getAlarmVolume(): number {
  const v = Number(localStorage.getItem(VOLUME_KEY));
  return Number.isFinite(v) && v > 0 && v <= 1 ? v : 0.7;
}

export function setAlarmVolume(v: number): void {
  localStorage.setItem(VOLUME_KEY, String(Math.min(1, Math.max(0, v))));
}

export function newAlarmId(): string {
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

const DEFAULT_ALARM: Omit<TimeAlarm, 'id'> = {
  enabled: true, kind: 'interval', everyMin: 60, startMin: 0,
  atMin: 0, weekdaysOnly: false, leadSec: 0, tone: 'chime', label: '',
};

function clampInt(v: unknown, lo: number, hi: number, def: number): number {
  const n = Math.round(Number(v));
  return Number.isFinite(n) ? Math.min(hi, Math.max(lo, n)) : def;
}

/**
 * Приводит запись к рабочему виду. Отдельная функция, а не доверие к JSON:
 * в хранилище лежит то, что записала прошлая версия страницы, и одна испорченная
 * запись не должна уронить расписание целиком.
 */
export function normalizeAlarm(raw: Partial<TimeAlarm>): TimeAlarm {
  return {
    ...DEFAULT_ALARM,
    id: typeof raw.id === 'string' && raw.id ? raw.id : newAlarmId(),
    enabled: raw.enabled !== false,
    kind: raw.kind === 'at' ? 'at' : 'interval',
    everyMin: clampInt(raw.everyMin, 1, 1440, 60),
    startMin: clampInt(raw.startMin, 0, 1439, 0),
    atMin: clampInt(raw.atMin, 0, 1439, 0),
    weekdaysOnly: raw.weekdaysOnly === true,
    leadSec: clampInt(raw.leadSec, 0, 600, 0),
    tone: TONES.some((t) => t.value === raw.tone) ? (raw.tone as AlarmTone) : 'chime',
    label: typeof raw.label === 'string' ? raw.label.slice(0, 60) : '',
  };
}

export function makeAlarm(patch: Partial<TimeAlarm> = {}): TimeAlarm {
  return normalizeAlarm({ ...DEFAULT_ALARM, ...patch, id: patch.id ?? newAlarmId() });
}

export function loadAlarms(): TimeAlarm[] {
  try {
    const raw = localStorage.getItem(ALARMS_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.map((a) => normalizeAlarm(a as Partial<TimeAlarm>));
  } catch {
    return [];
  }
}

export function saveAlarms(alarms: TimeAlarm[]): void {
  try {
    localStorage.setItem(ALARMS_KEY, JSON.stringify(alarms));
  } catch {
    // Хранилище переполнено — расписание доживёт хотя бы до конца сессии.
  }
}

/** Заготовки для кнопок быстрого добавления. */
export const ALARM_PRESETS: { label: string; patch: Partial<TimeAlarm> }[] = [
  { label: 'Каждый час', patch: { everyMin: 60 } },
  { label: 'Каждые полчаса', patch: { everyMin: 30 } },
  { label: 'Каждые 15 минут', patch: { everyMin: 15 } },
  { label: 'Каждые 2 часа', patch: { everyMin: 120 } },
];

// ── Звук ────────────────────────────────────────────────────────────────────

// note — одна нота осциллятором. Готовых файлов не держим: страница отдаётся
// под строгим CSP, а осциллятору внешние ресурсы не нужны.
function note(
  c: AudioContext, freq: number, at: number, len: number, vol: number,
  type: OscillatorType = 'sine',
): void {
  const osc = c.createOscillator();
  const gain = c.createGain();
  osc.type = type;
  osc.frequency.value = freq;
  const t0 = c.currentTime + at;
  // Края огибающей сглажены: прямоугольный импульс щёлкает в динамике.
  gain.gain.setValueAtTime(0.0001, t0);
  gain.gain.exponentialRampToValueAtTime(Math.max(0.001, vol), t0 + 0.015);
  gain.gain.exponentialRampToValueAtTime(0.0001, t0 + len);
  osc.connect(gain).connect(c.destination);
  osc.start(t0);
  osc.stop(t0 + len + 0.03);
}

function scheduleTone(c: AudioContext, tone: AlarmTone, vol: number): void {
  switch (tone) {
    case 'beep':
      note(c, 880, 0, 0.18, 0.5 * vol);
      break;
    case 'double':
      note(c, 880, 0, 0.12, 0.5 * vol);
      note(c, 1174.7, 0.16, 0.16, 0.5 * vol);
      break;
    case 'gong':
      note(c, 196, 0, 1.6, 0.6 * vol, 'triangle');
      note(c, 293.7, 0.02, 1.2, 0.35 * vol);
      break;
    default:
      note(c, 880, 0, 0.35, 0.5 * vol);
      note(c, 1318.5, 0.18, 0.4, 0.5 * vol);
  }
}

/** Проигрывает сигнал. Молча выходит, если звук браузером ещё не разрешён. */
export function playAlarmTone(tone: AlarmTone, volume = getAlarmVolume()): void {
  if (tone === 'custom') {
    const custom = getCustomSoundDataURL();
    if (custom) {
      const a = new Audio(custom);
      a.volume = volume;
      a.play().catch(() => {});
      return;
    }
    // Файл не загружен — лучше встроенный чайм, чем тишина вместо сигнала.
  }
  const c = alertAudioContext();
  if (!c) return;
  // У приостановленного контекста currentTime стоит на месте, и ноты,
  // запланированные до фактического запуска, оказались бы в прошлом.
  if (c.state === 'suspended') {
    c.resume().then(() => scheduleTone(c, tone, volume)).catch(() => {});
    return;
  }
  scheduleTone(c, tone, volume);
}
