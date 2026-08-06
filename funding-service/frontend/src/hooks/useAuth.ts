import { useEffect } from 'react';
import { useAuthStore } from '../store/authStore';

// Привязка происходит вне сайта — в Telegram. Чтобы вкладка узнала о ней сама,
// перечитываем состояние при возврате фокуса: человек уходит в мессенджер,
// жмёт «Старт» и возвращается на уже обновлённую страницу.
export function useAuthSync(): void {
  const refresh = useAuthStore((s) => s.refresh);

  useEffect(() => {
    void refresh();

    const onFocus = () => {
      if (document.visibilityState === 'visible') void refresh();
    };
    window.addEventListener('focus', onFocus);
    document.addEventListener('visibilitychange', onFocus);
    return () => {
      window.removeEventListener('focus', onFocus);
      document.removeEventListener('visibilitychange', onFocus);
    };
  }, [refresh]);
}
