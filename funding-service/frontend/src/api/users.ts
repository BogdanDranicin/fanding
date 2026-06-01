const USER_ID_KEY = 'user_id';
const USER_TOKEN_KEY = 'user_token';
const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? '';

export interface UserRecord {
  id: number;
  token: string;
}

export interface TelegramLinkResponse {
  url: string;
  linked: boolean;
}

export async function ensureUser(): Promise<UserRecord> {
  const storedId = sessionStorage.getItem(USER_ID_KEY);
  const storedToken = sessionStorage.getItem(USER_TOKEN_KEY);

  if (storedId && storedToken) {
    return { id: parseInt(storedId, 10), token: storedToken };
  }

  const res = await fetch(`${API_BASE}/api/v1/users`, { method: 'POST' });
  if (!res.ok) throw new Error('Failed to create user');

  const data: UserRecord = await res.json();
  sessionStorage.setItem(USER_ID_KEY, String(data.id));
  sessionStorage.setItem(USER_TOKEN_KEY, data.token);
  return data;
}

export async function getTelegramLink(userId: number, token: string): Promise<TelegramLinkResponse> {
  const params = new URLSearchParams({ token });
  const res = await fetch(`${API_BASE}/api/v1/users/${userId}/telegram-link?${params}`);
  if (!res.ok) throw new Error('Failed to get telegram link');
  return res.json();
}
