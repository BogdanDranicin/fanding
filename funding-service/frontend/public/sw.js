// Сервис-воркер: единственное, что в браузере просыпается, когда страница уже
// не просыпается.
//
// Браузер замораживает вкладку, которую не открывали минут пять: в ней не
// выполняются таймеры и не идёт звук, поставленный в очередь WebAudio заранее.
// Сигнал по времени при этом молчал — ровно на это и жаловались. Сервис-воркер
// к вкладке не привязан: push будит его и при замороженной вкладке, и при
// свёрнутом окне, и при закрытом браузере.

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

function parse(event) {
  try {
    return event.data ? event.data.json() : {};
  } catch {
    return {};
  }
}

// Если на страницу сейчас смотрят, она играет сигнал сама — своим тоном, своей
// громкостью, своим файлом. Уведомление в этом случае всё равно показывается
// (браузер иначе покажет своё «сайт работает в фоне»), но беззвучно: два звука
// на один сигнал — каша.
async function pageIsVisible() {
  const clients = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
  return clients.some((c) => c.visibilityState === 'visible');
}

self.addEventListener('push', (event) => {
  const data = parse(event);
  const title = data.title || 'Сигнал';
  event.waitUntil((async () => {
    const silent = await pageIsVisible();
    await self.registration.showNotification(title, {
      body: data.body || '',
      // Тег тот же, что у уведомления со страницы: повтор одного сигнала
      // заменяет предыдущее уведомление, а не копится стопкой.
      tag: data.tag || 'time-alarm',
      renotify: !silent,
      silent,
      icon: '/favicon.svg',
      badge: '/favicon.svg',
      timestamp: Date.now(),
    });
  })());
});

// Клик по уведомлению возвращает к уже открытой вкладке, а не заводит вторую.
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  event.waitUntil((async () => {
    const clients = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const client of clients) {
      if ('focus' in client) return client.focus();
    }
    return self.clients.openWindow('/');
  })());
});
