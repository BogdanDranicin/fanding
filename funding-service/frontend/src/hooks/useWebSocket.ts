import { useEffect, useRef } from 'react';
import { decode } from '@msgpack/msgpack';
import { useFundingStore } from '../store/fundingStore';
import { scheduleUpdate } from '../lib/rafBatch';
import type { FundingSnapshot, WSMessage } from '../types/funding';

const MIN_DELAY = 500;
const MAX_DELAY = 30_000;

export function useWebSocket(url: string): void {
  const setSnapshot = useFundingStore((s) => s.setSnapshot);
  const setWSStatus = useFundingStore((s) => s.setWSStatus);
  const wsRef = useRef<WebSocket | null>(null);
  const delayRef = useRef(MIN_DELAY);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const unmountedRef = useRef(false);

  useEffect(() => {
    unmountedRef.current = false;

    function connect() {
      if (unmountedRef.current) return;

      setWSStatus('connecting');
      const ws = new WebSocket(url);
      ws.binaryType = 'arraybuffer';
      wsRef.current = ws;

      ws.onopen = () => {
        delayRef.current = MIN_DELAY;
        setWSStatus('connected');
      };

      ws.onmessage = (evt: MessageEvent<ArrayBuffer>) => {
        try {
          const msg = decode(evt.data) as WSMessage;
          if (msg.type === 'snapshot') {
            const snap = msg.payload as FundingSnapshot;
            snap.ts = msg.ts;
            scheduleUpdate(() => setSnapshot(snap));
          }
        } catch {
          // malformed frame — ignore
        }
      };

      ws.onclose = () => {
        if (unmountedRef.current) return;
        setWSStatus('disconnected');
        timerRef.current = setTimeout(() => {
          delayRef.current = Math.min(delayRef.current * 2, MAX_DELAY);
          connect();
        }, delayRef.current);
      };

      ws.onerror = () => {
        ws.close();
      };
    }

    connect();

    return () => {
      unmountedRef.current = true;
      if (timerRef.current !== null) clearTimeout(timerRef.current);
      wsRef.current?.close();
    };
  }, [url, setSnapshot, setWSStatus]);
}
