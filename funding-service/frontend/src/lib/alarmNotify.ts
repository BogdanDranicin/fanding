// Уведомления операционной системы для сигналов по времени.
//
// Второй канал, а не замена звуку. Плашка на странице видна, только если на
// страницу смотрят; звук слышно, только если браузеру разрешено играть. Когда
// окно свёрнуто, а звук ещё не разблокирован жестом, единственным, что дойдёт
// до пользователя, остаётся системное уведомление.
//
// Разрешение не выпрашивается при загрузке страницы: браузеры справедливо
// считают это дурным тоном и Chrome такой запрос молча душит. Спрашиваем по
// кнопке на странице настроек — то есть в ответ на осознанное действие.

const NOTIFY_KEY = 'time_alarms_notify';

export function isNotifyEnabled(): boolean {
  return localStorage.getItem(NOTIFY_KEY) === '1';
}

function setNotifyEnabled(on: boolean): void {
  localStorage.setItem(NOTIFY_KEY, on ? '1' : '0');
}

/** Поддерживает ли браузер уведомления вообще. */
export function notifySupported(): boolean {
  return typeof Notification !== 'undefined';
}

/** Текущее разрешение: 'default' — ещё не спрашивали. */
export function notifyPermission(): NotificationPermission | 'unsupported' {
  return notifySupported() ? Notification.permission : 'unsupported';
}

/**
 * Просит разрешение и запоминает выбор. Возвращает, будут ли уведомления
 * показываться после этого.
 */
export async function enableNotifications(): Promise<boolean> {
  if (!notifySupported()) return false;
  let perm = Notification.permission;
  if (perm === 'default') {
    try {
      perm = await Notification.requestPermission();
    } catch {
      return false;
    }
  }
  const ok = perm === 'granted';
  setNotifyEnabled(ok);
  return ok;
}

/** Выключает уведомления. Само разрешение браузера отзывает только пользователь. */
export function disableNotifications(): void {
  setNotifyEnabled(false);
}

/**
 * Показывает уведомление о сработавшем сигнале. Молчит, если пользователь их не
 * включал или разрешение отозвано.
 *
 * tag — по одному уведомлению на сигнал: пачка пропущенных за ночь отметок не
 * должна завалить центр уведомлений одинаковыми строками.
 */
export function notifyAlarm(title: string, mark: string, ahead: boolean): void {
  if (!isNotifyEnabled() || !notifySupported()) return;
  if (Notification.permission !== 'granted') return;
  try {
    new Notification(title, {
      body: ahead ? `Скоро ${mark} МСК` : `${mark} МСК`,
      tag: `time-alarm:${title}`,
      silent: true, // звук у нас свой, системный поверх него — каша
    });
  } catch {
    // На некоторых платформах конструктор запрещён вне service worker —
    // уведомления там просто не будет, звук и плашка останутся.
  }
}
