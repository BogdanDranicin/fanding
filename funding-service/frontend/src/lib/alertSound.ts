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

// Браузеры блокируют звук до первого жеста пользователя. Разово подписываемся
// на pointerdown/keydown и «разогреваем» AudioContext, чтобы сигнал, пришедший
// позже по WebSocket, уже мог прозвучать.
export function initAlertUnlock(): void {
  const unlock = () => {
    ctx().resume().catch(() => {});
    window.removeEventListener('pointerdown', unlock);
    window.removeEventListener('keydown', unlock);
  };
  window.addEventListener('pointerdown', unlock);
  window.addEventListener('keydown', unlock);
}

/** Проигрывает сигнал: пользовательский файл, если задан, иначе встроенный чайм. */
export function playAlert(): void {
  const custom = localStorage.getItem(SOUND_KEY);
  if (custom) {
    const a = new Audio(custom);
    a.volume = getAlertVolume();
    a.play().catch(() => {});
    return;
  }
  playChime();
}

// Двойной двухтональный чайм (A5→E6), ~0.9 с.
function playChime(): void {
  let c: AudioContext;
  try {
    c = ctx();
  } catch {
    return; // WebAudio недоступен
  }
  c.resume().catch(() => {});
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
