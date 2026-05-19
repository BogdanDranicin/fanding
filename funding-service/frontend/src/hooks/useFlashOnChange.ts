import { useEffect, useRef, useState } from 'react';

type FlashClass = 'flash-up' | 'flash-down' | null;

export function useFlashOnChange(value: number | null | undefined): FlashClass {
  const prevRef = useRef<number | null | undefined>(undefined);
  const [flash, setFlash] = useState<FlashClass>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    const prev = prevRef.current;
    prevRef.current = value;

    if (prev === undefined || value == null || prev == null) return;
    if (value === prev) return;

    if (timerRef.current !== null) clearTimeout(timerRef.current);

    const cls: FlashClass = value > prev ? 'flash-up' : 'flash-down';
    setFlash(cls);
    timerRef.current = setTimeout(() => setFlash(null), 450);
  }, [value]);

  return flash;
}
