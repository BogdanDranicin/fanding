const SESSION_KEY = 'funding_session';
// Ключи старой схемы: пара id+token жила в sessionStorage и умирала вместе с
// вкладкой, из-за чего уже привязанному пользователю сайт снова показывал виджет
// «Привязать Telegram». Чистим их, чтобы не путались под ногами.
const LEGACY_KEYS = ['user_id', 'user_token'];

const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? '';

export interface Me {
  linked: boolean;
  is_admin: boolean;
  telegram_username: string;
}

export interface TelegramLink {
  url: string;
  linked: boolean;
  is_admin: boolean;
}

export const ANONYMOUS: Me = { linked: false, is_admin: false, telegram_username: '' };

function readToken(): string | null {
  try {
    return localStorage.getItem(SESSION_KEY);
  } catch {
    return null; // приватный режим без доступа к localStorage
  }
}

function writeToken(token: string): void {
  try {
    localStorage.setItem(SESSION_KEY, token);
    LEGACY_KEYS.forEach((k) => sessionStorage.removeItem(k));
  } catch {
    // Не смогли сохранить — сессия проживёт до перезагрузки, это не повод падать.
  }
}

async function createSession(): Promise<string> {
  const res = await fetch(`${API_BASE}/api/v1/session`, { method: 'POST' });
  if (!res.ok) throw new Error(`session: HTTP ${res.status}`);
  const data: { token: string } = await res.json();
  writeToken(data.token);
  return data.token;
}

// ensureSession возвращает токен текущего браузера, заводя сессию при первом визите.
export async function ensureSession(): Promise<string> {
  return readToken() ?? createSession();
}

// authFetch ходит в API с токеном сессии. На 401 (сессию удалили на сервере)
// один раз заводит новую и повторяет запрос — пользователь ничего не замечает.
export async function authFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const withToken = async (token: string) =>
    fetch(`${API_BASE}${path}`, {
      ...init,
      headers: { ...init.headers, Authorization: `Bearer ${token}` },
    });

  const res = await withToken(await ensureSession());
  if (res.status !== 401) return res;
  return withToken(await createSession());
}

// fetchMe — состояние текущей сессии: привязан ли Telegram и админ ли это.
export async function fetchMe(): Promise<Me> {
  const res = await authFetch('/api/v1/me');
  if (!res.ok) throw new Error(`me: HTTP ${res.status}`);
  return res.json();
}

// fetchTelegramLink возвращает deep-link для привязки. url === '' означает, что
// бот не настроен на сервере (нет TELEGRAM_BOT_USERNAME) — это не ошибка.
export async function fetchTelegramLink(): Promise<TelegramLink> {
  const res = await authFetch('/api/v1/me/telegram-link');
  if (res.status === 503) return { url: '', linked: false, is_admin: false };
  if (!res.ok) throw new Error(`telegram-link: HTTP ${res.status}`);
  return res.json();
}
