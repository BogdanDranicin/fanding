import { useEffect, useRef } from 'react';
import { useFundingStore } from '../store/fundingStore';
import { isAlertEnabled, playAlert } from '../lib/alertSound';

// Сигналит в момент, когда в снапшоте ПОЯВЛЯЕТСЯ точный фандинг (cb_funding):
// бэкенд заполняет его только после публикации курса ЦБ, до этого поля нет.
// Первый снапшот после загрузки страницы лишь запоминает состояние — иначе
// открытие сайта вечером (фандинг уже посчитан) давало бы ложный сигнал.
export function useFundingAlert(): void {
  const current = useFundingStore((s) => s.current);
  const prevPresent = useRef<boolean | null>(null);

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
