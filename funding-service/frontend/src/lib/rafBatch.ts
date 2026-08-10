// Пакетирование обновлений стора под кадр отрисовки: снапшоты приходят каждые
// 250 мс, перерисовывать чаще кадра смысла нет.
//
// ВАЖНО: requestAnimationFrame в СКРЫТОЙ вкладке браузер не вызывает вообще —
// ни Chrome, ни Firefox, ни Safari. WebSocket при этом продолжает получать
// кадры, поэтому обновления копились в pending и до стора не доезжали: звуковой
// сигнал «фандинг посчитан» молчал ровно тогда, когда он и нужен — когда
// пользователь смотрит в другое окно (и вкладка скрыта, и окно свёрнуто дают
// document.hidden). Побочно очередь росла без предела: 4 кадра/с за час — это
// больше 14 000 повисших замыканий.
//
// Когда вкладка скрыта, рисовать нечего — применяем обновление сразу, без кадра.

type UpdateFn = () => void;

let pending: UpdateFn[] = [];
let rafId: number | null = null;

function hidden(): boolean {
  return typeof document !== 'undefined' && document.hidden;
}

/** Применяет накопленные обновления немедленно и снимает запланированный кадр. */
function flush(): void {
  if (rafId !== null) {
    cancelAnimationFrame(rafId);
    rafId = null;
  }
  const toRun = pending;
  pending = [];
  for (const f of toRun) f();
}

export function scheduleUpdate(fn: UpdateFn): void {
  if (hidden()) {
    // Кадра не будет — иначе обновление зависнет в очереди до возврата на вкладку.
    fn();
    return;
  }
  pending.push(fn);
  if (rafId === null) {
    rafId = requestAnimationFrame(() => {
      rafId = null;
      flush();
    });
  }
}

// Вкладку могли скрыть в момент, когда кадр уже запланирован, но ещё не наступил:
// такой кадр не наступит никогда. Досрочно применяем всё, что в очереди.
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) flush();
  });
}
