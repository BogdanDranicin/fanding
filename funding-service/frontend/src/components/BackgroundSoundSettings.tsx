import { useEffect, useState } from 'react';
import { alertAudioContext } from '../lib/alertSound';
import {
  audioStatus,
  isKeepAliveEnabled,
  refreshKeepAlive,
  setKeepAliveEnabled,
  type AudioStatus,
} from '../lib/audioKeepAlive';
import {
  disableNotifications,
  enableNotifications,
  isNotifyEnabled,
  notifyPermission,
  notifySupported,
} from '../lib/alarmNotify';

/**
 * «Звук в свёрнутом окне» — настройки и, что важнее, честный статус.
 *
 * Раздел появился потому, что молчащий сигнал невозможно было отличить от
 * выключенного: браузер не разрешил звук — страница молчит, вкладка заморожена —
 * страница молчит, всё в порядке и просто ещё не время — страница тоже молчит.
 * Теперь видно, в каком из трёх состояний она находится.
 */
export function BackgroundSoundSettings() {
  const [keepAlive, setKeepAlive] = useState(isKeepAliveEnabled);
  const [notify, setNotify] = useState(isNotifyEnabled);
  const [status, setStatus] = useState<AudioStatus>(() => audioStatus(alertAudioContext()));

  // Состояние аудиоконтекста меняется само (разрешение после клика, сон машины),
  // и подписаться на него одним событием нельзя — опрашиваем раз в секунду.
  // Это страница настроек, её держат открытой минуту.
  useEffect(() => {
    const id = window.setInterval(() => setStatus(audioStatus(alertAudioContext())), 1000);
    return () => window.clearInterval(id);
  }, []);

  const toggleKeepAlive = (on: boolean) => {
    setKeepAliveEnabled(on);
    setKeepAlive(on);
    // Применяем сразу, не дожидаясь перезагрузки страницы. Список владельцев
    // удержания при этом не меняется — меняется только разрешение на него.
    refreshKeepAlive(alertAudioContext());
  };

  const toggleNotify = (on: boolean) => {
    if (!on) {
      disableNotifications();
      setNotify(false);
      return;
    }
    void enableNotifications().then(setNotify);
  };

  const perm = notifyPermission();

  return (
    <div className="settings-section">
      <h3>Звук в свёрнутом окне</h3>

      <p>
        Браузер останавливает вкладку, которую долго не смотрели: минут через
        пять после ухода в фон в ней перестают выполняться таймеры, а звук
        глохнет. Единственное исключение — вкладка, которая звук
        <b> проигрывает</b>. Поэтому, пока заведён хотя бы один сигнал, страница
        держит неслышимый тон 30 Гц: услышать его нельзя, а вкладка остаётся
        живой. На ярлыке вкладки при этом виден значок динамика — это и есть
        признак, по которому браузер решает её не усыплять.
      </p>

      <label className="settings-row">
        <input
          type="checkbox"
          checked={keepAlive}
          onChange={(e) => toggleKeepAlive(e.target.checked)}
        />
        <span>Держать вкладку активной, пока есть сигналы</span>
      </label>

      {notifySupported() && (
        <label className="settings-row">
          <input
            type="checkbox"
            checked={notify}
            disabled={perm === 'denied'}
            onChange={(e) => toggleNotify(e.target.checked)}
          />
          <span>
            Дублировать сигнал уведомлением системы
            {perm === 'denied' && ' — запрещено в настройках браузера'}
          </span>
        </label>
      )}

      <div className={`snd-status snd-${status.blocked ? 'bad' : 'ok'}`}>
        {status.state === 'unavailable' && 'Звук в этом браузере недоступен.'}
        {status.state === 'suspended'
          && 'Браузер ещё не разрешил звук. Щёлкните мышью по странице — один раз за сессию.'}
        {status.state === 'running' && !status.keepAlive && !keepAlive
          && 'Звук разрешён. Удержание вкладки выключено — в свёрнутом окне сигнал не гарантирован.'}
        {status.state === 'running' && !status.keepAlive && keepAlive
          && 'Звук разрешён. Удержание включится, как только появится хотя бы один сигнал.'}
        {status.state === 'running' && status.keepAlive
          && 'Звук разрешён, вкладка удерживается активной — сигнал прозвучит и в свёрнутом окне.'}
      </div>
    </div>
  );
}
