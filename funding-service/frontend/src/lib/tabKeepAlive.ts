// Удержание фоновой вкладки живой.
//
// Задача одна: пока пользователь чего-то ждёт (заведён сигнал по времени или
// включено уведомление о публикации фандинга), вкладка должна оставаться
// работоспособной, даже если её не открывали час. Мешает этому ровно одно
// поведение Chrome — ЗАМОРОЗКА (Page Lifecycle, состояние frozen): примерно
// через пять минут вне поля зрения браузер останавливает скрытую страницу
// целиком. У замороженной страницы не выполняется ни один таймер (ни
// setTimeout, ни таймер веб-воркера), не разбираются кадры WebSocket и стоит
// аудиоконтекст вместе с очередью заранее поставленных нот. Правкой расписания
// это не лечится: некому проснуться.
//
// Первое решение было грубым: страница непрерывно играла неслышимый тон на
// 30 Гц — играющую вкладку браузер не замораживает. Работало, но ценой значка
// динамика на вкладке, постоянно занятого аудиовыхода и разряда батареи:
// вкладка «звучала» сутками ради одного сигнала в день.
//
// Теперь удержание тихое. Chrome не замораживает страницу, которая держит
// Web Lock, — это записано в его собственном перечне причин не замораживать
// (chrome/browser/performance_manager/docs/freezing_opt_out_opt_in.md:
// «The page is currently holding a Web Lock or an IndexedDB transaction»).
// Лок берётся один на вкладку, в режиме shared: exclusive означал бы, что
// второе окно сайта встанет в очередь за первым и останется без защиты.
// Стоит удержание ничего — ни звука, ни значка, ни пробуждений процессора.
//
// Чего лок НЕ отменяет — торможения таймеров: у скрытой вкладки они будятся
// раз в секунду, а после пяти минут в фоне раз в минуту. Отменять и не надо:
// планировщик сигналов ставит звук в очередь WebAudio за десять минут до
// отметки, и одного пробуждения в минуту ему хватает с запасом. Важно было
// именно то, что пробуждения вообще есть.
//
// Запасной выход. Если браузер всё-таки заморозил страницу, он честно
// сообщает об этом событием `freeze` — мы его слушаем. Такая заморозка
// означает, что тихого удержания на этом браузере не хватило, и тогда (и
// только тогда) на неделю возвращается прежний неслышимый тон. Он же
// включается сразу, если Web Locks в браузере нет вовсе или если пользователь
// сам попросил его в настройках.
//
// Что осталось непокрытым честно: выгрузка вкладки из памяти (Memory Saver).
// Выгруженной страницы не существует — её не держит ни лок, ни тон; она
// перезагрузится при возвращении, и расписание поднимется из localStorage.

/** Пользователь сам включил тон в настройках. */
const TONE_KEY = 'tab_keepalive_tone';
/** Когда браузер последний раз заморозил страницу вопреки локу. */
const FROZE_KEY = 'tab_keepalive_froze_at';

/**
 * Сколько после замеченной заморозки держим запасной тон. Неделя — с одной
 * стороны, достаточно долго, чтобы разовая случайность не осталась
 * незамеченной пользователем; с другой — срок сам себя гасит, и одна странность
 * не приговаривает вкладку к вечному значку динамика.
 */
const FROZE_TTL_MS = 7 * 24 * 60 * 60 * 1000;

/** Имя лока. Одно на весь сайт: режим shared разрешает держать его всем вкладкам. */
const LOCK_NAME = 'funding-tab-alive';

/** Частота запасного тона, Гц. На нижней границе слышимости. */
const TONE_HZ = 30;

/**
 * Амплитуда запасного тона: достаточно велика, чтобы браузер считал вкладку
 * звучащей, и достаточно мала, чтобы её нельзя было услышать (−52 dBFS).
 */
const TONE_GAIN = 0.0025;

/** Чем вкладка удерживается прямо сейчас. */
export type KeepAliveMode = 'lock' | 'tone' | 'none';

export interface KeepAliveStatus {
  /** Есть ли ради чего держать вкладку. */
  wanted: boolean;
  mode: KeepAliveMode;
  /** Тон включён руками в настройках. */
  toneForced: boolean;
  /** Отметка последней заморозки вопреки локу; null — такого не было. */
  frozeAt: number | null;
  /** Поддерживает ли браузер Web Locks. */
  locks: boolean;
  audio: AudioContextState | 'unavailable';
  /** Браузер ещё не разрешил звук — нужен клик по странице. */
  blocked: boolean;
}

interface Tone {
  ctx: AudioContext;
  osc: OscillatorNode;
  gain: GainNode;
}

// Владельцы удержания. Их двое и они независимы: расписание сигналов по времени
// и уведомление о появлении точного фандинга. Множество, а не флаг, потому что
// выключение одного не должно ронять другой.
const owners = new Set<string>();

let ctxRef: AudioContext | null = null;
let releaseLock: (() => void) | null = null;
let lockPending = false;
let tone: Tone | null = null;
let bound = false;
// Страница уходит в bfcache: следующий `freeze` — не отказ удержания, а обычная
// навигация назад/вперёд, и записывать его в отказы нельзя.
let leaving = false;

function locksAvailable(): boolean {
  return typeof navigator !== 'undefined' && navigator.locks != null;
}

export function isToneForced(): boolean {
  try {
    return localStorage.getItem(TONE_KEY) === '1';
  } catch {
    return false;
  }
}

/** Включает или снимает запасной тон вручную. Действует сразу, без перезагрузки. */
export function setToneForced(on: boolean): void {
  try {
    if (on) localStorage.setItem(TONE_KEY, '1');
    else localStorage.removeItem(TONE_KEY);
  } catch {
    // Хранилище недоступно — настройка доживёт до конца сессии как есть.
  }
  apply();
}

function frozeAt(): number | null {
  try {
    const v = Number(localStorage.getItem(FROZE_KEY));
    return Number.isFinite(v) && v > 0 ? v : null;
  } catch {
    return null;
  }
}

function frozeRecently(): boolean {
  const at = frozeAt();
  return at !== null && Date.now() - at < FROZE_TTL_MS;
}

function toneNeeded(): boolean {
  return isToneForced() || !locksAvailable() || frozeRecently();
}

/**
 * Просит держать вкладку живой. Вызывать, когда есть ради чего: заведён сигнал
 * по времени или включено уведомление о фандинге. Повторные вызовы бесплатны.
 *
 * `ctx` нужен только запасному тону; при обычном тихом удержании он не трогается
 * вовсе — но пусть будет под рукой на случай отказа.
 */
export function keepTabAlive(ctx: AudioContext | null, owner = 'default'): void {
  if (ctx) ctxRef = ctx;
  owners.add(owner);
  bind();
  apply();
}

/** Снимает удержание за одного владельца. Отпускаем, когда уйдёт последний. */
export function releaseTabAlive(owner = 'default'): void {
  owners.delete(owner);
  apply();
}

/** Что происходит с удержанием — для страницы настроек. */
export function keepAliveStatus(ctx: AudioContext | null = ctxRef): KeepAliveStatus {
  return {
    wanted: owners.size > 0,
    mode: tone ? 'tone' : releaseLock ? 'lock' : 'none',
    toneForced: isToneForced(),
    frozeAt: frozeRecently() ? frozeAt() : null,
    locks: locksAvailable(),
    audio: ctx ? ctx.state : 'unavailable',
    blocked: !ctx || ctx.state !== 'running',
  };
}

function apply(): void {
  if (owners.size === 0) {
    dropLock();
    stopTone();
    return;
  }
  takeLock();
  if (toneNeeded()) startTone();
  else stopTone();
}

// ── Тихое удержание ─────────────────────────────────────────────────────────

function takeLock(): void {
  if (lockPending || releaseLock || !locksAvailable()) return;
  lockPending = true;
  // Лок держится, пока не разрешится промис тела запроса. Отдаём наружу его
  // resolve — это и есть «отпустить»; AbortSignal тут не помог бы, он снимает
  // только ещё не выданный запрос.
  void navigator.locks
    .request(LOCK_NAME, { mode: 'shared' }, () =>
      new Promise<void>((done) => {
        lockPending = false;
        releaseLock = () => {
          releaseLock = null;
          done();
        };
        // Владельцы успели уйти, пока лок ехал, — держать больше нечего.
        if (owners.size === 0) releaseLock();
      }),
    )
    .catch(() => {
      // Браузер отказал в локе — значит, тихого удержания на нём нет.
      lockPending = false;
      releaseLock = null;
      if (owners.size > 0) startTone();
    });
}

function dropLock(): void {
  releaseLock?.();
}

// ── Запасной тон ────────────────────────────────────────────────────────────

function startTone(): void {
  const c = ctxRef;
  if (tone || !c) return;
  if (c.state !== 'running') {
    // Разрешения на звук ещё нет. Просим — и уходим: подписки ниже поймают
    // переход в running и вернутся сюда сами.
    void c.resume().catch(() => {});
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
    // WebAudio недоступен — остаётся надеяться на лок.
  }
}

function stopTone(): void {
  if (!tone) return;
  try {
    tone.osc.stop();
    tone.osc.disconnect();
    tone.gain.disconnect();
  } catch {
    // Уже остановлен.
  }
  tone = null;
}

// ── Подписки ────────────────────────────────────────────────────────────────

/**
 * Заморозка вопреки локу. Кода после этого обработчика не выполняется — всё,
 * что можно, это оставить запись; тон поднимется на `resume`, когда браузер
 * вернёт страницу к жизни, и не даст заморозить её снова.
 */
function onFreeze(): void {
  if (leaving || owners.size === 0) return;
  try {
    localStorage.setItem(FROZE_KEY, String(Date.now()));
  } catch {
    // Не записалось — тон включится хотя бы на эту сессию.
  }
}

/**
 * Возвращение страницы к жизни и любые события, после которых состояние могло
 * измениться: разморозка, выход из bfcache, показ вкладки, жест пользователя
 * (единственный момент, когда браузер точно разрешит звук).
 */
function revive(): void {
  if (owners.size === 0) return;
  const c = ctxRef;
  if (c && c.state === 'suspended') {
    // Приостановленный контекст роняет и тон: держать ссылку на остановленный
    // осциллятор незачем, после resume он не оживёт.
    stopTone();
    void c.resume().then(apply, () => {});
  }
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
  window.addEventListener('pointerdown', revive);
  window.addEventListener('keydown', revive);
  window.addEventListener('touchstart', revive);
  ctxRef?.addEventListener('statechange', revive);
}

/** Только для тестов: сбрасывает состояние модуля между прогонами. */
export function __resetKeepAliveForTests(): void {
  stopTone();
  dropLock();
  owners.clear();
  ctxRef = null;
  lockPending = false;
  bound = false;
  leaving = false;
}

/** Только для тестов: заморозка и возвращение страницы к жизни. */
export function __freezeForTests(): void {
  onFreeze();
  revive();
}

/** Только для тестов: страница уходит в bfcache (pagehide). */
export function __leavingForTests(): void {
  leaving = true;
}

/** Только для тестов: жест пользователя или показ вкладки. */
export function __reviveForTests(): void {
  revive();
}
