// Звуковой сигнал «точный фандинг посчитан».
// Настройки живут в localStorage: включён ли сигнал, громкость и (опционально)
// пользовательский звук как data-URL. Без пользовательского файла играет
// встроенный двухтональный чайм через WebAudio — внешних ассетов не нужно.

const ENABLED_KEY = 'funding_alert_enabled';
const VOLUME_KEY = 'funding_alert_volume';
const SOUND_KEY = 'funding_alert_sound';
const SOUND_NAME_KEY = 'funding_alert_sound_name';

// data-URL раздувает файл на ~33%, а квота localStorage — около 5 МБ на origin,
// в которой живут и остальные настройки. 2 МБ исходника — безопасный потолок.
export const MAX_SOUND_BYTES = 2 * 1024 * 1024;

export function isAlertEnabled(): boolean {
  return localStorage.getItem(ENABLED_KEY) !== '0';
}

export function setAlertEnabled(on: boolean): void {
  localStorage.setItem(ENABLED_KEY, on ? '1' : '0');
}

export function getAlertVolume(): number {
  const v = Number(localStorage.getItem(VOLUME_KEY));
  return Number.isFinite(v) && v > 0 && v <= 1 ? v : 0.8;
}

export function setAlertVolume(v: number): void {
  localStorage.setItem(VOLUME_KEY, String(Math.min(1, Math.max(0, v))));
}

export function getCustomSoundName(): string | null {
  return localStorage.getItem(SOUND_KEY) ? localStorage.getItem(SOUND_NAME_KEY) : null;
}

export function clearCustomSound(): void {
  localStorage.removeItem(SOUND_KEY);
  localStorage.removeItem(SOUND_NAME_KEY);
}

/** Сохраняет пользовательский звук. Бросает Error с человекочитаемым текстом. */
export function setCustomSound(file: File): Promise<void> {
  if (!file.type.startsWith('audio/')) {
    return Promise.reject(new Error('Нужен аудиофайл (mp3, ogg, wav…)'));
  }
  if (file.size > MAX_SOUND_BYTES) {
    return Promise.reject(new Error('Файл больше 2 МБ — возьмите короткий сигнал'));
  }
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error('Не удалось прочитать файл'));
    reader.onload = () => {
      try {
        localStorage.setItem(SOUND_KEY, reader.result as string);
        localStorage.setItem(SOUND_NAME_KEY, file.name);
        resolve();
      } catch {
        reject(new Error('Не хватило места в хранилище браузера — файл меньше, пожалуйста'));
      }
    };
    reader.readAsDataURL(file);
  });
}

let audioCtx: AudioContext | null = null;

function ctx(): AudioContext {
  if (!audioCtx) audioCtx = new AudioContext();
  return audioCtx;
}

/**
 * Общий AudioContext страницы. Один на всё: браузер разрешает звук после жеста
 * пользователя именно контексту, и второй, заведённый где-то ещё, оказался бы
 * заблокированным. null — WebAudio в этом браузере недоступен.
 */
export function alertAudioContext(): AudioContext | null {
  try {
    return ctx();
  } catch {
    return null;
  }
}

/** Загруженный пользователем звук как data-URL; null — файла нет. */
export function getCustomSoundDataURL(): string | null {
  return localStorage.getItem(SOUND_KEY);
}

// Браузеры блокируют звук до первого жеста пользователя. Подписываемся на
// pointerdown/keydown и «разогреваем» AudioContext, чтобы сигнал, пришедший
// позже по WebSocket, уже мог прозвучать.
//
// Слушатели НЕ снимаются после первого жеста: браузер может приостановить
// контекст и позже (долго скрытая вкладка, спящий ноутбук), и тогда единственный
// шанс поднять его — следующий жест пользователя. resume() на уже запущенном
// контексте бесплатен, так что держать подписку дешевле, чем ловить немой сигнал.
let unlockBound = false;

export function initAlertUnlock(): void {
  if (unlockBound) return;
  unlockBound = true;
  const unlock = () => {
    try {
      ctx().resume().catch(() => {});
    } catch {
      // WebAudio недоступен — молча выходим
    }
  };
  window.addEventListener('pointerdown', unlock);
  window.addEventListener('keydown', unlock);
  window.addEventListener('touchstart', unlock);
}

/** Проигрывает сигнал: пользовательский файл, если задан, иначе встроенный чайм. */
export function playAlert(): void {
  const custom = localStorage.getItem(SOUND_KEY);
  if (!custom) {
    playChime();
    return;
  }
  // Пользовательский файл идёт через WebAudio, а не через элемент <audio>.
  // Причина не в красоте: страница отдаётся под CSP `default-src 'self'`,
  // который закрывает и media-src, поэтому data-URL в <audio> браузер блокирует
  // молча — у всех, кто загрузил свой звук, сигнал фандинга был беззвучным.
  // Декодированный буфер к сети отношения не имеет и под запрет не попадает.
  let c: AudioContext;
  try {
    c = ctx();
  } catch {
    return;
  }
  const play = (buf: AudioBuffer) => {
    const src = c.createBufferSource();
    const gain = c.createGain();
    src.buffer = buf;
    gain.gain.value = getAlertVolume();
    src.connect(gain).connect(c.destination);
    src.start();
  };
  const run = () => {
    void decodeCustom(c, custom).then((buf) => {
      if (buf) play(buf);
      else playChime(); // файл не читается — лучше встроенный чайм, чем тишина
    });
  };
  if (c.state === 'suspended') c.resume().then(run, () => {});
  else run();
}

// Декодированный пользовательский звук держим в памяти: разбор base64 и
// decodeAudioData на двухмегабайтном файле стоят заметно дороже самого сигнала.
let customBuf: { url: string; buf: AudioBuffer } | null = null;

function decodeCustom(c: AudioContext, url: string): Promise<AudioBuffer | null> {
  if (customBuf && customBuf.url === url) return Promise.resolve(customBuf.buf);
  const comma = url.indexOf(',');
  if (comma < 0) return Promise.resolve(null);
  let bytes: ArrayBuffer;
  try {
    const bin = atob(url.slice(comma + 1));
    const arr = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
    bytes = arr.buffer;
  } catch {
    return Promise.resolve(null);
  }
  return c.decodeAudioData(bytes).then(
    (buf) => {
      customBuf = { url, buf };
      return buf;
    },
    () => null,
  );
}

// Двойной двухтональный чайм (A5→E6), ~0.9 с.
function playChime(): void {
  let c: AudioContext;
  try {
    c = ctx();
  } catch {
    return; // WebAudio недоступен
  }
  // resume() асинхронный, а у приостановленного контекста currentTime стоит на
  // месте. Раньше ноты планировались сразу после вызова resume — от замороженного
  // currentTime, — и к моменту реального запуска оказывались в прошлом: сигнал
  // глох или сминался в один щелчок. Планируем только на запущенном контексте.
  if (c.state === 'suspended') {
    c.resume().then(() => scheduleChime(c)).catch(() => {});
    return;
  }
  scheduleChime(c);
}

function scheduleChime(c: AudioContext): void {
  const vol = getAlertVolume();
  const note = (freq: number, at: number) => {
    const osc = c.createOscillator();
    const gain = c.createGain();
    osc.type = 'sine';
    osc.frequency.value = freq;
    gain.gain.setValueAtTime(0, c.currentTime + at);
    gain.gain.linearRampToValueAtTime(0.5 * vol, c.currentTime + at + 0.02);
    gain.gain.exponentialRampToValueAtTime(0.001, c.currentTime + at + 0.35);
    osc.connect(gain).connect(c.destination);
    osc.start(c.currentTime + at);
    osc.stop(c.currentTime + at + 0.4);
  };
  note(880, 0);
  note(1318.5, 0.18);
  note(880, 0.5);
  note(1318.5, 0.68);
}
