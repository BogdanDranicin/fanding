import { useEffect, useRef } from 'react';
import { useFundingStore } from '../store/fundingStore';
import { alertAudioContext, isAlertEnabled, playAlert } from '../lib/alertSound';
import { keepTabAlive, releaseTabAlive } from '../lib/tabKeepAlive';

// Сигналит в момент, когда в снапшоте ПОЯВЛЯЕТСЯ точный фандинг (cb_funding):
// бэкенд заполняет его только после публикации курса ЦБ, до этого поля нет.
// Первый снапшот после загрузки страницы лишь запоминает состояние — иначе
// открытие сайта вечером (фандинг уже посчитан) давало бы ложный сигнал.
//
// Сигнал ждут именно в свёрнутом окне: публикация ЦБ приходит между 16:30 и
// 18:00, и сидеть эти полтора часа на вкладке никто не будет. Поэтому, пока
// уведомление включено, вкладка удерживается от заморозки (см. tabKeepAlive):
// иначе браузер морозит её через пять минут, и в момент публикации в ней не
// выполняется ни одной строки кода — ни разбор кадра WebSocket, ни звук.
export function useFundingAlert(): void {
  const current = useFundingStore((s) => s.current);
  const prevPresent = useRef<boolean | null>(null);

  useEffect(() => {
    if (!isAlertEnabled()) return;
    keepTabAlive(alertAudioContext(), 'funding-alert');
    return () => releaseTabAlive('funding-alert');
  }, []);

  useEffect(() => {
    if (!current) return;
    const present =
      current.USDRUBF.cb_funding != null || current.EURRUBF.cb_funding != null;
    if (prevPresent.current === false && present && isAlertEnabled()) {
      playAlert();
    }
    prevPresent.current = present;
  }, [current]);
}
