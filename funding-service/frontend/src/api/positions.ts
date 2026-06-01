import type { Position, BrokerConnectionStatus } from '../types/funding';

const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? '';

export async function fetchPositions(): Promise<Position[]> {
  const res = await fetch(`${API_BASE}/api/v1/positions`);
  if (res.status === 503) throw new Error('not_configured');
  if (!res.ok) throw new Error(`positions fetch failed: ${res.status}`);
  return res.json();
}

export async function getConnectionStatus(): Promise<BrokerConnectionStatus> {
  const res = await fetch(`${API_BASE}/api/v1/settings/positions/status`);
  if (!res.ok) throw new Error('status fetch failed');
  return res.json();
}

export async function saveConnectionSettings(
  ssoSession: string,
  deviceId: string,
): Promise<{ expires_at: string }> {
  const res = await fetch(`${API_BASE}/api/v1/settings/positions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sso_session: ssoSession, device_id: deviceId }),
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || 'save failed');
  }
  return res.json();
}
