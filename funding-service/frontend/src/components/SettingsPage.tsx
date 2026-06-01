import { useEffect, useState } from 'react';
import { ensureUser, getTelegramLink } from '../api/users';
import { getConnectionStatus, saveConnectionSettings } from '../api/positions';

interface Props {
  onBack: () => void;
}

const DEFAULT_DEVICE_ID = '55d211e4-50af-40e0-8de0-83ab8ab348ab';

export function SettingsPage({ onBack }: Props) {
  const [tgUrl, setTgUrl] = useState<string | null>(null);
  const [linked, setLinked] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [ssoSession, setSsoSession] = useState('');
  const [deviceId, setDeviceId] = useState(DEFAULT_DEVICE_ID);
  const [connStatus, setConnStatus] = useState<{ configured: boolean; expires_at?: string } | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saveOk, setSaveOk] = useState(false);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      try {
        const user = await ensureUser();
        const link = await getTelegramLink(user.id, user.token);
        const status = await getConnectionStatus();
        if (!cancelled) {
          setTgUrl(link.url);
          setLinked(link.linked);
          setConnStatus(status);
        }
      } catch (e) {
        if (!cancelled) setError(String(e));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();

    return () => { cancelled = true; };
  }, []);

  const handleSave = async () => {
    setSaving(true);
    setSaveError(null);
    setSaveOk(false);
    try {
      const result = await saveConnectionSettings(ssoSession, deviceId);
      setConnStatus({ configured: true, expires_at: result.expires_at });
      setSsoSession('');
      setSaveOk(true);
    } catch (e: unknown) {
      setSaveError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
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
        <h3>Брокерские позиции</h3>
        <p>Отображение активных позиций с tradersdiaries.com.</p>

        {connStatus?.configured && (
          <div style={{ color: 'var(--accent-up)', marginBottom: 12, fontSize: 13 }}>
            ✓ Подключено
            {connStatus.expires_at && ` · истекает ${connStatus.expires_at}`}
          </div>
        )}

        <div style={{ marginBottom: 10 }}>
          <label style={{ display: 'block', fontSize: 12, color: 'var(--text-muted)', marginBottom: 4 }}>
            SSO Session *
          </label>
          <textarea
            value={ssoSession}
            onChange={e => setSsoSession(e.target.value)}
            rows={2}
            placeholder="Вставьте значение cookie sso_session из DevTools"
            style={{
              width: '100%', boxSizing: 'border-box', resize: 'vertical',
              fontFamily: 'monospace', fontSize: 11,
              background: 'var(--bg-primary)', color: 'var(--text-primary)',
              border: '1px solid var(--border)', borderRadius: 4, padding: '6px 8px',
            }}
          />
        </div>

        <div style={{ marginBottom: 14 }}>
          <label style={{ display: 'block', fontSize: 12, color: 'var(--text-muted)', marginBottom: 4 }}>
            Device ID *
          </label>
          <input
            type="text"
            value={deviceId}
            onChange={e => setDeviceId(e.target.value)}
            style={{
              width: '100%', boxSizing: 'border-box',
              fontFamily: 'monospace', fontSize: 11,
              background: 'var(--bg-primary)', color: 'var(--text-primary)',
              border: '1px solid var(--border)', borderRadius: 4, padding: '6px 8px',
            }}
          />
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 12 }}>
          <button
            className="nav-link"
            onClick={handleSave}
            disabled={saving || !ssoSession.trim()}
          >
            {saving ? 'Сохранение…' : 'Сохранить'}
          </button>
          {saveOk && <span style={{ color: 'var(--accent-up)', fontSize: 12 }}>✓ Сохранено</span>}
          {saveError && <span style={{ color: 'var(--accent-down)', fontSize: 12 }}>{saveError}</span>}
        </div>

        <details style={{ fontSize: 12 }}>
          <summary style={{ cursor: 'pointer', color: 'var(--accent-up)', marginBottom: 8 }}>
            Как получить значения?
          </summary>
          <ol style={{ color: 'var(--text-muted)', lineHeight: 2, paddingLeft: 16 }}>
            <li>Откройте <a href="https://tradersdiaries.com" target="_blank" rel="noreferrer" style={{ color: 'var(--accent-up)' }}>tradersdiaries.com</a> и войдите в аккаунт</li>
            <li>Нажмите F12 → вкладка Application → Cookies</li>
            <li>Выберите <code>id-api.tradersdiaries.com</code></li>
            <li>Скопируйте значение <strong>sso_session</strong> в поле выше</li>
          </ol>
        </details>
      </div>
    </div>
  );
}
