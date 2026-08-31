import { useEffect, useRef, useState } from 'react';
import { fetchTelegramLink } from '../api/auth';
import { useAuthStore } from '../store/authStore';
import { TimeAlarmsSettings } from './TimeAlarmsSettings';
import {
  alertAudioContext,
  clearCustomSound,
  getAlertVolume,
  getCustomSoundName,
  isAlertEnabled,
  playAlert,
  setAlertEnabled,
  setAlertVolume,
  setCustomSound,
} from '../lib/alertSound';
import {
  isToneForced,
  keepAliveStatus,
  setToneForced,
  type KeepAliveStatus,
} from '../lib/tabKeepAlive';

interface Props {
  onBack: () => void;
}

/** Одной строкой: чем вкладка удерживается прямо сейчас. */
function aliveText(a: KeepAliveStatus): string {
  if (!a.wanted) return 'удерживать нечего — ни сигналов, ни ожидания фандинга';
  if (a.mode === 'tone') return 'неслышимый звук (значок динамика на вкладке)';
  if (a.mode === 'lock') return 'тихое удержание, Web Lock';
  return a.locks ? 'удержание не взято' : 'браузер не умеет Web Lock';
}

function frozeDate(ms: number): string {
  const d = new Date(ms);
  return `${String(d.getDate()).padStart(2, '0')}.${String(d.getMonth() + 1).padStart(2, '0')}`;
}

export function SettingsPage({ onBack }: Props) {
  const me = useAuthStore((s) => s.me);
  const authLoading = useAuthStore((s) => s.loading);
  const authError = useAuthStore((s) => s.error);
  const refreshAuth = useAuthStore((s) => s.refresh);

  const [tgUrl, setTgUrl] = useState<string | null>(null);
  const [linkError, setLinkError] = useState<string | null>(null);

  const [soundOn, setSoundOn] = useState(isAlertEnabled);
  const [volume, setVolume] = useState(getAlertVolume);
  const [soundName, setSoundName] = useState<string | null>(getCustomSoundName);
  const [soundError, setSoundError] = useState<string | null>(null);
  const [tone, setTone] = useState(isToneForced);
  const [alive, setAlive] = useState<KeepAliveStatus | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  // Ссылка нужна только непривязанному — за привязанного всё уже сказал /api/v1/me.
  useEffect(() => {
    if (authLoading || me.linked) return;
    let cancelled = false;

    (async () => {
      try {
        const link = await fetchTelegramLink();
        if (!cancelled) {
          setTgUrl(link.url);
          setLinkError(null);
        }
      } catch (e) {
        if (!cancelled) setLinkError(e instanceof Error ? e.message : String(e));
      }
    })();

    return () => { cancelled = true; };
  }, [authLoading, me.linked]);

  // Привязка делается в Telegram, в другом окне. Пока пользователь не привязан и
  // смотрит на эту страницу, спрашиваем сервер раз в 3 секунды: вернувшись из
  // мессенджера, он видит «✓ привязан», не перезагружая сайт.
  useEffect(() => {
    if (authLoading || me.linked) return;
    const id = setInterval(() => { void refreshAuth(); }, 3000);
    return () => clearInterval(id);
  }, [authLoading, me.linked, refreshAuth]);

  const toggleSound = (on: boolean) => {
    setAlertEnabled(on);
    setSoundOn(on);
  };

  const changeVolume = (v: number) => {
    setAlertVolume(v);
    setVolume(v);
  };

  const pickFile = async (file: File | undefined) => {
    if (!file) return;
    setSoundError(null);
    try {
      await setCustomSound(file);
      setSoundName(file.name);
      playAlert(); // сразу дать услышать выбранный звук
    } catch (e) {
      setSoundError(e instanceof Error ? e.message : String(e));
    } finally {
      if (fileRef.current) fileRef.current.value = '';
    }
  };

  const resetSound = () => {
    clearCustomSound();
    setSoundName(null);
    setSoundError(null);
  };

  const toggleTone = (on: boolean) => {
    setToneForced(on);
    setTone(on);
    setAlive(keepAliveStatus(alertAudioContext()));
  };

  // Удержание заводится эффектами в корне приложения — на первом рендере этой
  // страницы его ещё нет, и прочитанное состояние соврало бы. К тому же оно
  // меняется само: браузер разрешает звук по клику, лок выдаётся не мгновенно,
  // сигналы включают и выключают на соседней вкладке. Дешевле переспрашивать.
  useEffect(() => {
    const read = () => setAlive(keepAliveStatus(alertAudioContext()));
    read();
    const id = setInterval(read, 2000);
    return () => clearInterval(id);
  }, []);

  return (
    <div className="settings-page">
      <div className="settings-nav">
        <button className="nav-link" onClick={onBack}>← Назад</button>
        <h2>Настройки</h2>
      </div>

      <div className="settings-section">
        <h3>Telegram-уведомления</h3>
        <p>Получайте мгновенные уведомления при публикации нового официального курса ЦБ.</p>

        {authLoading && <span style={{ color: 'var(--text-muted)' }}>Загрузка…</span>}
        {!authLoading && authError && (
          <span style={{ color: 'var(--accent-down)' }}>Ошибка: {authError}</span>
        )}
        {!authLoading && !authError && me.linked && (
          <span style={{ color: 'var(--accent-up)' }}>
            ✓ Telegram привязан{me.telegram_username && ` (@${me.telegram_username})`}
            {me.is_admin && ' · админ'}
          </span>
        )}
        {!authLoading && !authError && !me.linked && tgUrl && (
          <a href={tgUrl} target="_blank" rel="noreferrer" className="btn-tg">
            Привязать Telegram
          </a>
        )}
        {!authLoading && !authError && !me.linked && tgUrl === '' && (
          <span style={{ color: 'var(--text-muted)' }}>
            Бот не настроен (TELEGRAM_BOT_USERNAME не задан)
          </span>
        )}
        {!authLoading && !authError && !me.linked && linkError && (
          <span style={{ color: 'var(--accent-down)' }}>Ошибка: {linkError}</span>
        )}
      </div>

      <div className="settings-section">
        <h3>Звуковой сигнал</h3>
        <p>
          Звук проигрывается в момент, когда точный фандинг посчитан
          (опубликован новый курс ЦБ). Вкладка с сайтом должна быть открыта,
          а страница — хотя бы раз получить клик: браузеры не дают играть звук
          без взаимодействия с пользователем.
        </p>

        <label className="settings-row">
          <input
            type="checkbox"
            checked={soundOn}
            onChange={(e) => toggleSound(e.target.checked)}
          />
          <span>Проигрывать звук при публикации фандинга</span>
        </label>

        <label className="settings-row">
          <span className="settings-row-label">Громкость</span>
          <input
            type="range"
            min={0.05}
            max={1}
            step={0.05}
            value={volume}
            disabled={!soundOn}
            onChange={(e) => changeVolume(Number(e.target.value))}
          />
        </label>

        <div className="settings-row">
          <span className="settings-row-label">Свой звук</span>
          <input
            ref={fileRef}
            type="file"
            accept="audio/*"
            style={{ display: 'none' }}
            onChange={(e) => pickFile(e.target.files?.[0])}
          />
          <button
            className="btn-plain"
            disabled={!soundOn}
            onClick={() => fileRef.current?.click()}
          >
            {soundName ? 'Заменить…' : 'Выбрать файл…'}
          </button>
          {soundName && (
            <>
              <span className="settings-sound-name" title={soundName}>{soundName}</span>
              <button className="btn-plain" onClick={resetSound}>Сбросить</button>
            </>
          )}
          {!soundName && (
            <span style={{ color: 'var(--text-muted)', fontSize: 13 }}>встроенный чайм</span>
          )}
        </div>
        {soundError && (
          <span style={{ color: 'var(--accent-down)', fontSize: 13 }}>{soundError}</span>
        )}

        <div>
          <button className="btn-plain" disabled={!soundOn} onClick={() => playAlert()}>
            ▶ Проверить звук
          </button>
        </div>
      </div>

      <div className="settings-section">
        <h3>Вкладка в фоне</h3>
        <p>
          Браузер замораживает вкладку, которую не открывали минут пять: в ней
          перестают выполняться таймеры, и сигнал не приходит вовсе. Пока чего-то
          ждём — публикации фандинга или сигнала по времени, — страница держит
          Web Lock: браузер такую вкладку не морозит, и стоит это ничего.
        </p>

        <div className="settings-row">
          <span className="settings-row-label">Сейчас</span>
          <span style={{ color: 'var(--text-muted)', fontSize: 13 }}>
            {alive ? aliveText(alive) : '…'}
          </span>
        </div>

        {alive?.frozeAt != null && (
          <span style={{ color: 'var(--text-muted)', fontSize: 13 }}>
            Браузер всё-таки заморозил вкладку {frozeDate(alive.frozeAt)} — на неделю
            включён запасной неслышимый звук.
          </span>
        )}

        <label className="settings-row">
          <input type="checkbox" checked={tone} onChange={(e) => toggleTone(e.target.checked)} />
          <span>
            Держать вкладку неслышимым звуком (надёжнее всего, но на вкладке
            появится значок динамика)
          </span>
        </label>
      </div>

      <TimeAlarmsSettings />
    </div>
  );
}
