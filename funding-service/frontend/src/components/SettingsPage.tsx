import { useEffect, useRef, useState } from 'react';
import { ensureUser, getTelegramLink } from '../api/users';
import {
  clearCustomSound,
  getAlertVolume,
  getCustomSoundName,
  isAlertEnabled,
  playAlert,
  setAlertEnabled,
  setAlertVolume,
  setCustomSound,
} from '../lib/alertSound';

interface Props {
  onBack: () => void;
}

export function SettingsPage({ onBack }: Props) {
  const [tgUrl, setTgUrl] = useState<string | null>(null);
  const [linked, setLinked] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [soundOn, setSoundOn] = useState(isAlertEnabled);
  const [volume, setVolume] = useState(getAlertVolume);
  const [soundName, setSoundName] = useState<string | null>(getCustomSoundName);
  const [soundError, setSoundError] = useState<string | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      try {
        const user = await ensureUser();
        const link = await getTelegramLink(user.id, user.token);
        if (!cancelled) {
          setTgUrl(link.url);
          setLinked(link.linked);
        }
      } catch (e) {
        if (!cancelled) setError(String(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();

    return () => { cancelled = true; };
  }, []);

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

  return (
    <div className="settings-page">
      <div className="settings-nav">
        <button className="nav-link" onClick={onBack}>← Назад</button>
        <h2>Настройки</h2>
      </div>

      <div className="settings-section">
        <h3>Telegram-уведомления</h3>
        <p>Получайте мгновенные уведомления при публикации нового официального курса ЦБ.</p>

        {loading && <span style={{ color: 'var(--text-muted)' }}>Загрузка…</span>}
        {error && <span style={{ color: 'var(--accent-down)' }}>Ошибка: {error}</span>}
        {!loading && !error && linked && (
          <span style={{ color: 'var(--accent-up)' }}>✓ Telegram привязан</span>
        )}
        {!loading && !error && !linked && tgUrl && (
          <a href={tgUrl} target="_blank" rel="noreferrer" className="btn-tg">
            Привязать Telegram
          </a>
        )}
        {!loading && !error && !tgUrl && (
          <span style={{ color: 'var(--text-muted)' }}>
            Бот не настроен (TELEGRAM_BOT_USERNAME не задан)
          </span>
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

    </div>
  );
}
