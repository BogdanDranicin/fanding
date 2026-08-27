// Удержание звука в фоновой вкладке.
//
// Почему это отдельный модуль, а не пара строк в планировщике сигналов.
// Жалоба звучала так: «звук работает, но если окно долго не было активно —
// перестаёт». Это не одна поломка, а две, и обе — не наши баги, а поведение
// браузера, которое нельзя обойти правильным кодом таймеров:
//
//  1. ЗАМОРОЗКА ВКЛАДКИ. Chrome останавливает скрытую вкладку целиком примерно
//     через пять минут после того, как она ушла из поля зрения (Page Lifecycle,
//     состояние frozen). У замороженной страницы не выполняется НИ ОДИН таймер —
//     ни setTimeout, ни setInterval, ни таймер веб-воркера. Планировщик,
//     который ставит звук в очередь за полторы минуты до отметки, в такой
//     вкладке просто не просыпается, чтобы его поставить.
//  2. ОСТАНОВКА АУДИОКОНТЕКСТА. У вкладки, которая долго ничего не играет,
//     браузер приостанавливает AudioContext. У приостановленного контекста
//     стоят часы (currentTime), и ноты, назначенные на будущий момент по этим
//     часам, не звучат вовсе — они ждут времени, которое не наступает.
//
// От обеих бед спасает одно и то же: вкладку, КОТОРАЯ ИГРАЕТ ЗВУК, Chrome не
// замораживает и не душит её таймеры, а её аудиоконтекст не останавливает.
// Поэтому пока заведён хотя бы один сигнал, страница непрерывно проигрывает
// сигнал ниже порога слышимости: синус на 30 Гц с амплитудой 0.0025. Тридцать
// герц — нижняя граница человеческого слуха, и на такой громкости его не
// воспроизведёт ни ноутбучный динамик, ни наушники на разумной громкости; для
// браузера же это полноценный звук, и вкладка остаётся живой.
//
// Цена честная и её видно: на вкладке появляется значок динамика. Это не помеха,
// а ровно тот признак, по которому браузер решает не усыплять страницу. Кому
// значок мешает — выключается в настройках, но тогда сигналы в свёрнутом окне
// снова становятся лотереей.

const KEEPALIVE_KEY = 'audio_keepalive_enabled';

/** Частота удерживающего тона, Гц. Ниже порога слышимости. */
const KEEPALIVE_HZ = 30;

/**
 * Амплитуда удерживающего тона. Достаточно велика, чтобы браузер считал вкладку
 * звучащей, и достаточно мала, чтобы её нельзя было услышать: −52 dBFS на 30 Гц.
 */
const KEEPALIVE_GAIN = 0.0025;

export function isKeepAliveEnabled(): boolean {
  return localStorage.getItem(KEEPALIVE_KEY) !== '0';
}

export function setKeepAliveEnabled(on: boolean): void {
  localStorage.setItem(KEEPALIVE_KEY, on ? '1' : '0');
}

interface KeepAlive {
  ctx: AudioContext;
  osc: OscillatorNode;
  gain: GainNode;
}

let running: KeepAlive | null = null;
// Владельцы удержания. Их двое и они независимы: расписание сигналов по времени
// и уведомление о появлении точного фандинга. Счётчик, а не флаг, потому что
// выключение одного не должно ронять другой.
const owners = new Set<string>();
let watching = false;

function wanted(): boolean {
  return owners.size > 0;
}

/**
 * Состояние звука для страницы настроек: звучит ли удержание и в каком состоянии
 * аудиоконтекст. `blocked` означает, что браузер ещё не разрешил звук и нужен
 * клик по странице.
 */
export interface AudioStatus {
  keepAlive: boolean;
  state: AudioContextState | 'unavailable';
  blocked: boolean;
}

export function audioStatus(ctx: AudioContext | null): AudioStatus {
  if (!ctx) return { keepAlive: false, state: 'unavailable', blocked: true };
  return {
    keepAlive: running !== null,
    state: ctx.state,
    blocked: ctx.state !== 'running',
  };
}

/**
 * Просит удерживать звук живым. Вызывать, когда есть ради чего: заведён сигнал
 * по времени или включено уведомление о фандинге. Повторные вызовы бесплатны.
 *
 * Пока браузер не разрешил звук (не было ни одного клика по странице),
 * удержание не запускается — но подписка на разрешение остаётся, и удержание
 * включится само, как только контекст оживёт.
 */
export function keepAudioAlive(ctx: AudioContext | null, owner = 'default'): void {
  owners.add(owner);
  if (!ctx) return;
  watch(ctx);
  start(ctx);
}

/**
 * Пересматривает удержание после смены настройки: включает тон, если он теперь
 * разрешён и кому-то нужен, и гасит, если запрещён. Владельцев не трогает.
 */
export function refreshKeepAlive(ctx: AudioContext | null): void {
  if (!ctx) return;
  if (!isKeepAliveEnabled()) {
    stop();
    return;
  }
  watch(ctx);
  start(ctx);
}

/** Снимает удержание за одного владельца. Тон гаснет, когда уйдёт последний. */
export function releaseAudio(owner = 'default'): void {
  owners.delete(owner);
  if (!wanted()) stop();
}

function start(ctx: AudioContext): void {
  if (running || !wanted() || !isKeepAliveEnabled()) return;
  if (ctx.state !== 'running') {
    // Разрешения ещё нет. Просим — и уходим: наблюдатель ниже поймает переход
    // в running и запустит удержание сам.
    void ctx.resume().catch(() => {});
    return;
  }
  try {
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.type = 'sine';
    osc.frequency.value = KEEPALIVE_HZ;
    gain.gain.value = KEEPALIVE_GAIN;
    osc.connect(gain).connect(ctx.destination);
    osc.start();
    running = { ctx, osc, gain };
  } catch {
    // WebAudio недоступен — сигналы будут работать как раньше, без гарантий
    // в свёрнутом окне.
  }
}

function stop(): void {
  if (!running) return;
  try {
    running.osc.stop();
    running.osc.disconnect();
    running.gain.disconnect();
  } catch {
    // Уже остановлен.
  }
  running = null;
}

/**
 * Следит за контекстом и поднимает его обратно. Браузер приостанавливает
 * аудиоконтекст не только при уходе вкладки в фон: то же самое делает выход
 * машины из сна и смена аудиоустройства. Единственный надёжный способ это
 * пережить — подписаться на statechange и на все события возвращения к
 * странице, а не проверять состояние раз в N секунд.
 */
function watch(ctx: AudioContext): void {
  if (watching) return;
  watching = true;

  const revive = () => {
    if (!wanted()) return;
    if (ctx.state === 'running') {
      start(ctx);
      return;
    }
    // Приостановленный контекст роняет и удержание: держать ссылку на
    // остановленный осциллятор незачем, после resume он не оживёт.
    stop();
    void ctx.resume().then(() => start(ctx)).catch(() => {});
  };

  ctx.addEventListener('statechange', revive);
  document.addEventListener('visibilitychange', revive);
  window.addEventListener('focus', revive);
  window.addEventListener('pageshow', revive);
  // Жест пользователя — единственный момент, когда браузер точно разрешит звук.
  window.addEventListener('pointerdown', revive);
  window.addEventListener('keydown', revive);
  window.addEventListener('touchstart', revive);
}

/** Только для тестов: сбрасывает состояние модуля между прогонами. */
export function __resetKeepAliveForTests(): void {
  stop();
  owners.clear();
  watching = false;
}
