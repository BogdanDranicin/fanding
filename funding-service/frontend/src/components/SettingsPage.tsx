import { useEffect, useState } from 'react';
import { ensureUser, getTelegramLink } from '../api/users';

interface Props {
  onBack: () => void;
}

export function SettingsPage({ onBack }: Props) {
  const [tgUrl, setTgUrl] = useState<string | null>(null);
  const [linked, setLinked] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      try {
        const user = await ensureUser();
        const link = await getTelegramLink(user.id);
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

  return (
    <div className="settings-page">
      <div className="settings-nav">
        <button className="nav-link" onClick={onBack}>← Назад</button>
        <h2>Настройки</h2>
      </div>

      <div className="settings-section">
        <h3>Telegram-уведомления</h3>
        <p>
          Получайте мгновенные уведомления при публикации нового официального курса ЦБ.
        </p>

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
    </div>
  );
}
