// Push-уведомления: как сигнал доходит до вкладки, которую браузер заморозил.
//
// Расписание по-прежнему считает страница — она же и играет звук, пока жива:
// свой файл, своя громкость, свой тон на каждый сигнал. Push — второй канал, на
// случай, когда играть некому: браузер замораживает вкладку, которую не
// открывали минут пять, и в ней не выполняется ни один таймер. Тогда будильник
// звонит уведомлением операционной системы, разбуженным сервис-воркером.
//
// Поэтому наверх уезжает не расписание, а его выжимка — ближайшие отметки. Так
// серверу не нужно знать ни про шаги, ни про выходные, ни про предупреждения за
// N секунд, а правка расписания доезжает одним запросом.

import { authFetch } from '../api/auth';
import { isAlertEnabled } from './alertSound';

const ENABLED_KEY = 'push_enabled';
const ENDPOINT_KEY = 'push_endpoint';

export interface PushConfig {
  enabled: boolean;
  publicKey: string;
}

export interface PushWakeup {
  /** Момент срабатывания, миллисекунды эпохи. */
  at: number;
  title: string;
  body: string;
  tag: string;
}

export type PushState =
  | 'unsupported'   // браузер не умеет push
  | 'unavailable'   // сервис не настроен: ключей VAPID нет
  | 'denied'        // пользователь запретил уведомления
  | 'off'           // можно включить
  | 'on';

export function pushSupported(): boolean {
  return typeof navigator !== 'undefined'
    && 'serviceWorker' in navigator
    && 'PushManager' in window
    && typeof Notification !== 'undefined';
}

export function isPushEnabled(): boolean {
  try {
    return localStorage.getItem(ENABLED_KEY) === '1';
  } catch {
    return false;
  }
}

function setEnabled(on: boolean, endpoint = ''): void {
  try {
    localStorage.setItem(ENABLED_KEY, on ? '1' : '0');
    if (on) localStorage.setItem(ENDPOINT_KEY, endpoint);
    else localStorage.removeItem(ENDPOINT_KEY);
  } catch {
    // Приватный режим без localStorage — подписка доживёт до перезагрузки.
  }
}

function savedEndpoint(): string {
  try {
    return localStorage.getItem(ENDPOINT_KEY) ?? '';
  } catch {
    return '';
  }
}

let configCache: PushConfig | null = null;

/** Настроен ли push на сервере и с каким ключом. Ответ не меняется — кэшируем. */
export async function pushConfig(): Promise<PushConfig> {
  if (configCache) return configCache;
  try {
    const r = await authFetch('/api/v1/push/key');
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    const data = (await r.json()) as { enabled: boolean; public_key: string };
    configCache = { enabled: data.enabled, publicKey: data.public_key };
  } catch {
    configCache = { enabled: false, publicKey: '' };
  }
  return configCache;
}

export async function pushState(): Promise<PushState> {
  if (!pushSupported()) return 'unsupported';
  if (!(await pushConfig()).enabled) return 'unavailable';
  if (Notification.permission === 'denied') return 'denied';
  return isPushEnabled() ? 'on' : 'off';
}

// applicationServerKey браузер принимает только байтами, а сервер отдаёт ключ
// строкой base64url — переводим сами: atob не знает ни «-», ни «_».
function keyBytes(b64url: string): ArrayBuffer {
  const pad = '='.repeat((4 - (b64url.length % 4)) % 4);
  const raw = atob((b64url + pad).replace(/-/g, '+').replace(/_/g, '/'));
  const buf = new ArrayBuffer(raw.length);
  const out = new Uint8Array(buf);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return buf;
}

function b64(buf: ArrayBuffer | null): string {
  if (!buf) return '';
  const bytes = new Uint8Array(buf);
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

async function registration(): Promise<ServiceWorkerRegistration> {
  return navigator.serviceWorker.register('/sw.js', { scope: '/' });
}

/**
 * Включает уведомления: спрашивает разрешение, ставит сервис-воркер, подписывает
 * браузер и отдаёт подписку серверу. Возвращает, вышло ли.
 *
 * Разрешение спрашивается только отсюда — то есть по кнопке в настройках. При
 * загрузке страницы такой вопрос браузеры справедливо считают дурным тоном, а
 * Chrome его и вовсе душит.
 */
export async function enablePush(): Promise<boolean> {
  if (!pushSupported()) return false;
  const cfg = await pushConfig();
  if (!cfg.enabled) return false;

  let perm = Notification.permission;
  if (perm === 'default') {
    try {
      perm = await Notification.requestPermission();
    } catch {
      return false;
    }
  }
  if (perm !== 'granted') {
    setEnabled(false);
    return false;
  }

  try {
    const reg = await registration();
    // Уже выданную подписку переиспользуем: новая на том же ключе всё равно
    // вернула бы тот же адрес, а отписка-подписка на ровном месте — это окно,
    // в котором уведомления не придут.
    const sub = (await reg.pushManager.getSubscription())
      ?? (await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: keyBytes(cfg.publicKey),
      }));

    const body = {
      endpoint: sub.endpoint,
      keys: {
        p256dh: b64(sub.getKey('p256dh')),
        auth: b64(sub.getKey('auth')),
      },
      // Звук о публикации фандинга — отдельная галочка в настройках, и сервер
      // должен знать её состояние: уведомление о публикации он шлёт сам, а
      // спросить страницу в этот момент не у кого — она может быть закрыта.
      funding: isAlertEnabled(),
    };
    const r = await authFetch('/api/v1/push/subscribe', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    setEnabled(true, sub.endpoint);
    return true;
  } catch {
    setEnabled(false);
    return false;
  }
}

/**
 * Пересылает подписку заново — например, когда переключили галочку звука о
 * публикации фандинга. Выключенные уведомления оставляет выключенными.
 */
export async function refreshPushSubscription(): Promise<void> {
  if (!isPushEnabled()) return;
  await enablePush();
}

/** Выключает уведомления и убирает подписку с сервера. */
export async function disablePush(): Promise<void> {
  const endpoint = savedEndpoint();
  setEnabled(false);
  try {
    const reg = await navigator.serviceWorker.getRegistration('/');
    const sub = await reg?.pushManager.getSubscription();
    if (sub) await sub.unsubscribe();
  } catch {
    // Браузер уже снял подписку сам — на сервере её всё равно чистим.
  }
  if (!endpoint) return;
  try {
    await authFetch('/api/v1/push/unsubscribe', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ endpoint }),
    });
  } catch {
    // Сервер не ответил — строка умрёт сама на первом же 410 от push-сервиса.
  }
}

// Расписание перекладываем только когда оно действительно изменилось: страница
// пересчитывает его на каждом такте планировщика, а это раз в полминуты, и слать
// одно и то же в базу незачем.
let lastSent = '';

/**
 * Отдаёт серверу ближайшие отметки расписания. Тихо выходит, если уведомления
 * выключены: это фоновая работа, и мешать ею пользователю нечем.
 */
export async function syncWakeups(wakeups: PushWakeup[]): Promise<void> {
  if (!isPushEnabled()) {
    lastSent = '';
    return;
  }
  const endpoint = savedEndpoint();
  if (!endpoint) return;

  const payload = JSON.stringify({ endpoint, wakeups });
  if (payload === lastSent) return;

  try {
    const r = await authFetch('/api/v1/push/wakeups', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: payload,
    });
    if (r.status === 404) {
      // Сервер не знает этой подписки: базу чистили или сессия сменилась.
      // Подписываемся заново — расписание уедет следующим тактом.
      lastSent = '';
      await enablePush();
      return;
    }
    lastSent = r.ok ? payload : '';
  } catch {
    lastSent = '';
  }
}

/**
 * Просит сервер прислать проверочное уведомление. Возвращает, через сколько
 * секунд его ждать; ноль — не вышло.
 */
export async function sendPushTest(): Promise<number> {
  const endpoint = savedEndpoint();
  if (!endpoint) return 0;
  try {
    const r = await authFetch('/api/v1/push/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ endpoint }),
    });
    if (!r.ok) return 0;
    const data = (await r.json()) as { in_seconds: number };
    return data.in_seconds ?? 0;
  } catch {
    return 0;
  }
}

/** Забывает отправленное: следующий sync уедет, даже если список не изменился. */
export function resetWakeupSync(): void {
  lastSent = '';
}
