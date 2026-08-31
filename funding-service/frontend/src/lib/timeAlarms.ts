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

// ── Планирование ────────────────────────────────────────────────────────────
//
// Первая версия просто опрашивала расписание раз в полсекунды. В активной
// вкладке это работало, в фоновой — нет: браузер режет таймеры скрытой страницы
// до одного пробуждения в секунду, а через пять минут — до одного в минуту.
// Отсюда обе жалобы: сигнал приходил с опозданием, а иногда не приходил вовсе
// (опоздание перекрывало срок годности, и сигнал молча проглатывался).
//
// Поэтому звук больше не ждёт своего такта: он заранее ставится в очередь
// WebAudio, а таймер страницы остаётся только для бухгалтерии — посчитать
// следующую отметку и показать плашку.

/** Насколько просроченный сигнал ещё имеет смысл проиграть. */
export const ALARM_STALE_MS = 60_000;

/**
 * За сколько до отметки звук уходит в очередь WebAudio.
 *
 * Было полторы минуты — с расчётом на самый тугой режим фоновой вкладки, одно
 * пробуждение таймера в минуту. Этого хватало ровно до того момента, когда
 * браузер вкладку не душит, а ЗАМОРАЖИВАЕТ (после ~5 минут вне поля зрения):
 * у замороженной страницы таймеров нет вообще, и поставить звук за полторы
 * минуты она не успевает, потому что не просыпается ни разу.
 *
 * Теперь горизонт — десять минут, а от заморозки страницу держит Web Lock
 * (см. tabKeepAlive). Расхождение часов аудиопотока и системных на таком плече —
 * единицы миллисекунд, и планировщик всё равно сверяет его на каждом такте
 * (ALARM_DRIFT_TOLERANCE_MS).
 */
export const ALARM_ARM_MS = 600_000;

/**
 * Насколько поставленному звуку позволено разойтись со стенными часами, прежде
 * чем его снимут и поставят заново. Расхождение означает не неточность, а
 * остановку: у приостановленного аудиоконтекста часы стоят, и нота, назначенная
 * по ним, уезжает в будущее ровно на длительность остановки. Заодно ловится
 * перевод системных часов и выход машины из сна.
 */
export const ALARM_DRIFT_TOLERANCE_MS = 250;

/** Потолок сна планировщика: страховка от съехавших часов и смены суток. */
export const ALARM_MAX_SLEEP_MS = 30_000;

/** Стоит ли ещё звонить об отметке, которая уже наступила. */
export function isAlarmFresh(at: number, now: number): boolean {
  return now - at <= ALARM_STALE_MS;
}

/**
 * Через сколько планировщику проснуться ради отметки `at`: пока звук не в
 * очереди — к моменту постановки, после — сразу за отметкой, чтобы показать
 * плашку. Ноль отметки означает, что срабатываний нет, — тогда только фоновый
 * удар на всякий случай.
 */
export function alarmSleep(at: number, armed: boolean, now: number): number {
  if (!at) return ALARM_MAX_SLEEP_MS;
  // +20 мс: просыпаться ровно в момент отметки — значит с равной вероятностью
  // проснуться на миллисекунду раньше неё и уйти на второй круг впустую.
  const wake = armed ? at + 20 : at - ALARM_ARM_MS;
  return Math.min(ALARM_MAX_SLEEP_MS, Math.max(0, wake - now));
}

// ── Звук ────────────────────────────────────────────────────────────────────

/** Поставленный в очередь сигнал, который ещё можно снять. */
export interface ScheduledTone {
  cancel(): void;
  /**
   * На сколько миллисекунд запланированный момент разошёлся со стенными часами.
   * Ноль — звук ещё не поставлен либо идёт точно по расписанию; большая
   * величина означает, что часы аудиопотока стояли (вкладка спала) и звук надо
   * ставить заново.
   */
  driftMs(now?: number): number;
}

const SILENT: ScheduledTone = { cancel() {}, driftMs: () => 0 };

// note — одна нота осциллятором. Готовых файлов не держим: страница отдаётся
// под строгим CSP, а осциллятору внешние ресурсы не нужны.
function note(
  c: AudioContext, freq: number, at: number, len: number, vol: number,
  type: OscillatorType = 'sine',
): OscillatorNode {
  const osc = c.createOscillator();
  const gain = c.createGain();
  osc.type = type;
  osc.frequency.value = freq;
  // Края огибающей сглажены: прямоугольный импульс щёлкает в динамике.
  gain.gain.setValueAtTime(0.0001, at);
  gain.gain.exponentialRampToValueAtTime(Math.max(0.001, vol), at + 0.015);
  gain.gain.exponentialRampToValueAtTime(0.0001, at + len);
  osc.connect(gain).connect(c.destination);
  osc.start(at);
  osc.stop(at + len + 0.03);
  return osc;
}

/** Ставит тон на момент `at` по часам аудиоконтекста. */
function scheduleTone(c: AudioContext, tone: AlarmTone, vol: number, at: number): ScheduledTone {
  const parts: AudioScheduledSourceNode[] = [];
  switch (tone) {
    case 'beep':
      parts.push(note(c, 880, at, 0.18, 0.5 * vol));
      break;
    case 'double':
      parts.push(note(c, 880, at, 0.12, 0.5 * vol));
      parts.push(note(c, 1174.7, at + 0.16, 0.16, 0.5 * vol));
      break;
    case 'gong':
      parts.push(note(c, 196, at, 1.6, 0.6 * vol, 'triangle'));
      parts.push(note(c, 293.7, at + 0.02, 1.2, 0.35 * vol));
      break;
    default:
      parts.push(note(c, 880, at, 0.35, 0.5 * vol));
      parts.push(note(c, 1318.5, at + 0.18, 0.4, 0.5 * vol));
  }
  return { cancel: () => stopAll(parts), driftMs: () => 0 };
}

function stopAll(parts: AudioScheduledSourceNode[]): void {
  for (const p of parts) {
    try {
      p.stop();
      p.disconnect();
    } catch {
      // Нота уже отыграла — снимать нечего.
    }
  }
}

// Пользовательский файл тоже играем через WebAudio, а не элементом <audio>:
// во-первых, только у аудиоконтекста есть часы, по которым звук можно назначить
// на будущий момент; во-вторых, data-URL в <audio> запрещён нашим же CSP
// (`default-src 'self'` закрывает и media-src), а декодированный буфер к сети
// отношения не имеет.
let customBuf: { url: string; buf: AudioBuffer } | null = null;

function dataUrlBytes(url: string): ArrayBuffer | null {
  const comma = url.indexOf(',');
  if (comma < 0) return null;
  try {
    const bin = atob(url.slice(comma + 1));
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return bytes.buffer;
  } catch {
    return null;
  }
}

/** Декодированный пользовательский звук; null — файла нет или он не читается. */
function customBuffer(c: AudioContext): Promise<AudioBuffer | null> {
  const url = getCustomSoundDataURL();
  if (!url) return Promise.resolve(null);
  if (customBuf && customBuf.url === url) return Promise.resolve(customBuf.buf);
  const bytes = dataUrlBytes(url);
  if (!bytes) return Promise.resolve(null);
  return c.decodeAudioData(bytes).then(
    (buf) => {
      customBuf = { url, buf };
      return buf;
    },
    () => null,
  );
}

function scheduleBuffer(c: AudioContext, buf: AudioBuffer, vol: number, at: number): ScheduledTone {
  const src = c.createBufferSource();
  const gain = c.createGain();
  src.buffer = buf;
  gain.gain.value = Math.min(1, Math.max(0, vol));
  src.connect(gain).connect(c.destination);
  src.start(at);
  return { cancel: () => stopAll([src]), driftMs: () => 0 };
}

/**
 * Ставит сигнал на момент `atMs` (шкала Date.now()). Ноты назначаются по часам
 * WebAudio: они идут на аудиопотоке, который браузер не усыпляет вместе со
 * скрытой вкладкой, — поэтому сигнал звучит в свою миллисекунду, даже если
 * таймеры страницы к этому времени будятся раз в минуту.
 */
export function scheduleAlarmTone(
  tone: AlarmTone, volume = getAlarmVolume(), atMs = Date.now(),
): ScheduledTone {
  const c = alertAudioContext();
  if (!c) return SILENT;

  let live = true;
  let queued: ScheduledTone = SILENT;
  // Момент по часам аудиопотока, на который назначен звук. Нужен, чтобы потом
  // сверить его со стенными часами: разойтись они могут только одним способом —
  // если аудиоконтекст стоял.
  let audioTarget: number | null = null;

  // Момент считаем в последний момент перед постановкой: между вызовом и
  // фактическим запуском контекста может пройти заметное время.
  const audioAt = () => {
    audioTarget = c.currentTime + Math.max(0, atMs - Date.now()) / 1000;
    return audioTarget;
  };

  const arm = () => {
    if (!live) return;
    if (tone === 'custom') {
      void customBuffer(c).then((buf) => {
        if (!live) return;
        // Файл не загружен или не читается — лучше встроенный чайм, чем тишина.
        queued = buf
          ? scheduleBuffer(c, buf, volume, audioAt())
          : scheduleTone(c, 'chime', volume, audioAt());
      });
      return;
    }
    queued = scheduleTone(c, tone, volume, audioAt());
  };

  // У приостановленного контекста currentTime стоит на месте, и ноты,
  // назначенные до фактического запуска, оказались бы в прошлом.
  if (c.state === 'suspended') c.resume().then(arm, () => {});
  else arm();

  return {
    cancel() {
      live = false;
      queued.cancel();
    },
    driftMs(now = Date.now()) {
      if (audioTarget === null) return 0;
      // Когда звук зазвучит по стенным часам, если аудиочасы пойдут дальше
      // ровно так же, как идут сейчас.
      const willFireAt = now + (audioTarget - c.currentTime) * 1000;
      return willFireAt - atMs;
    },
  };
}

/** Проигрывает сигнал сразу. Молча выходит, если звук браузером ещё не разрешён. */
export function playAlarmTone(tone: AlarmTone, volume = getAlarmVolume()): void {
  scheduleAlarmTone(tone, volume, Date.now());
}
