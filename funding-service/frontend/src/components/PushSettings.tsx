import { useCallback, useEffect, useState } from 'react';
import {
  disablePush,
  enablePush,
  pushState,
  resetWakeupSync,
  sendPushTest,
  type PushState,
} from '../lib/push';
import { keepAliveStatus, type KeepAliveStatus } from '../lib/tabKeepAlive';

/**
 * «Сигнал в фоне»: единственная настройка, от которой зависит, прозвучит ли
 * будильник, когда на вкладку не смотрят.
 *
 * Раньше здесь была развилка из подпорок — держать вкладку локом, держать её
 * неслышимым звуком, — и обе не работали: браузер всё равно замораживает
 * вкладку, которую не открывали минут пять, а в замороженной странице не идут
 * ни таймеры, ни звук, поставленный в очередь заранее. Push эту развилку
 * убирает: сервер будит браузер сам, и вкладке для этого жить не обязательно.
 */
function stateText(state: PushState): string {
  switch (state) {
    case 'on':
      return 'включены — сигнал придёт даже при закрытом браузере';
    case 'denied':
      return 'запрещены в самом браузере: разрешите уведомления для сайта в его настройках';
    case 'unavailable':
      return 'на сервере не настроены (нет ключей VAPID)';
    case 'unsupported':
      return 'этот браузер их не умеет';
    default:
      return 'выключены — в свёрнутом окне сигнал может не прозвучать';
  }
}

function frozeDate(ms: number): string {
  const d = new Date(ms);
  return `${String(d.getDate()).padStart(2, '0')}.${String(d.getMonth() + 1).padStart(2, '0')}`;
}

export function PushSettings() {
  const [state, setState] = useState<PushState>('off');
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState('');
  const [alive, setAlive] = useState<KeepAliveStatus | null>(null);

  const refresh = useCallback(() => {
    void pushState().then(setState);
  }, []);

  // Удержание вкладки заводится эффектами в корне приложения, разрешение на
  // уведомления пользователь может отозвать в браузере, не заходя сюда, — и то
  // и другое дешевле переспрашивать, чем угадывать.
  useEffect(() => {
    const read = () => {
      setAlive(keepAliveStatus());
      refresh();
    };
    read();
    const id = setInterval(read, 3000);
    return () => clearInterval(id);
  }, [refresh]);

  const toggle = async (on: boolean) => {
    setBusy(true);
    setNote('');
    try {
      if (on) {
        const ok = await enablePush();
        // Расписание уедет ближайшим тактом планировщика, а не когда-нибудь:
        // без сброса он считает, что уже всё отправил.
        resetWakeupSync();
        if (!ok) setNote('Не вышло: браузер не дал разрешение или сервер не ответил.');
      } else {
        await disablePush();
      }
    } finally {
      setBusy(false);
      refresh();
    }
  };

  const test = async () => {
    setBusy(true);
    const sec = await sendPushTest();
    setBusy(false);
    setNote(sec > 0
      ? `Уведомление придёт через ${sec} с — сверните окно и проверьте, что оно доходит.`
      : 'Проверка не ушла: подписка не найдена, включите уведомления заново.');
  };

  const on = state === 'on';
  const canToggle = state === 'on' || state === 'off';

  return (
    <div className="settings-section">
      <h3>Сигнал в фоне</h3>
      <p>
        Браузер замораживает вкладку, которую не открывали минут пять: в ней
        перестают работать таймеры, и звук, поставленный в очередь заранее, не
        играет. Push этого не касается — уведомление будит сервис-воркер, даже
        когда вкладка заморожена, окно свёрнуто или браузер закрыт совсем.
      </p>
      <p>
        Пока на страницу смотрят, сигнал играет она сама — вашим файлом, вашей
        громкостью и своим тоном на каждый сигнал; уведомление в этот момент
        приходит беззвучно, чтобы не было двух звуков на один сигнал.
      </p>

      <label className="settings-row">
        <input
          type="checkbox"
          checked={on}
          disabled={!canToggle || busy}
          onChange={(e) => void toggle(e.target.checked)}
        />
        <span>Присылать сигналы уведомлениями браузера</span>
      </label>

      <div className="settings-row">
        <span className="settings-row-label">Сейчас</span>
        <span style={{ color: 'var(--text-muted)', fontSize: 13 }}>{stateText(state)}</span>
      </div>

      {on && (
        <div>
          <button className="btn-plain" disabled={busy} onClick={() => void test()}>
            ▶ Проверить уведомление
          </button>
        </div>
      )}

      {note && <span style={{ color: 'var(--text-muted)', fontSize: 13 }}>{note}</span>}

      {/* Удержание вкладки осталось как было и работает, пока браузер его
          слушается. Это уже не единственная надежда, поэтому и рассказывать о
          нём подробно незачем — одна строка состояния. */}
      {alive?.wanted && (
        <span style={{ color: 'var(--text-dim)', fontSize: 12.5 }}>
          Вкладку заодно держим Web Lock: {alive.mode === 'lock' ? 'удержание взято' : 'удержания нет'}
          {alive.frozeAt != null && `, последняя заморозка ${frozeDate(alive.frozeAt)}`}.
        </span>
      )}
    </div>
  );
}
