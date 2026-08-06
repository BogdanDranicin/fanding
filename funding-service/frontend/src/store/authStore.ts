import { create } from 'zustand';
import { ANONYMOUS, fetchMe, type Me } from '../api/auth';

interface AuthState {
  me: Me;
  loading: boolean;
  error: string | null;
  refresh: () => Promise<void>;
}

// Один источник правды о текущем пользователе на всё приложение: и шапка (какие
// вкладки показывать), и страница настроек (привязан ли Telegram) читают его.
export const useAuthStore = create<AuthState>((set) => ({
  me: ANONYMOUS,
  loading: true,
  error: null,

  refresh: async () => {
    try {
      set({ me: await fetchMe(), error: null });
    } catch (e) {
      set({ error: e instanceof Error ? e.message : String(e) });
    } finally {
      set({ loading: false });
    }
  },
}));
