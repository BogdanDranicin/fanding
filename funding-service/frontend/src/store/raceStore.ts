import { create } from 'zustand';
import { persist } from 'zustand/middleware';

export interface CBRRaceResult {
  source_id: string;
  name: string;
  desc: string;
  rate_date: string;
  timestamp?: string;
  usd_rub: number;
  eur_rub: number;
  cny_rub: number;
  latency_ms: number;
  error?: string;
}

export interface RaceRound {
  id: number;
  startedAt: string; // ISO string
  results: CBRRaceResult[];
}

// Per source: first time we saw it showing a date newer than priorDate
export interface SourceDetection {
  source_id: string;
  name: string;
  firstSeenAt: string; // ISO string — startedAt of the detecting round
  latency_ms: number;
}

export function parseDateNum(d: string): number {
  if (!d || d.length < 10) return 0;
  if (d[2] === '.') return parseInt(d.slice(6, 10) + d.slice(3, 5) + d.slice(0, 2), 10);
  if (d[4] === '-') return parseInt(d.slice(0, 4) + d.slice(5, 7) + d.slice(8, 10), 10);
  return 0;
}

interface RaceState {
  rounds: RaceRound[];
  roundCounter: number;
  // Date from the very first round — the baseline we're waiting to surpass
  priorDate: string;
  // Newest rate_date seen so far (may equal priorDate if no new publication yet)
  latestDate: string;
  // sourceId → first detection of a date *newer* than priorDate
  detections: Record<string, SourceDetection>;

  addRound: (startedAt: Date, results: CBRRaceResult[]) => void;
  clearHistory: () => void;
}

export const useRaceStore = create<RaceState>()(
  persist(
    (set, get) => ({
      rounds: [],
      roundCounter: 0,
      priorDate: '',
      latestDate: '',
      detections: {},

      addRound: (startedAt, results) => {
        const state = get();
        const id = state.roundCounter + 1;
        const iso = startedAt.toISOString();

        // Find the max date in this round
        let maxNum = 0;
        let maxDate = '';
        for (const r of results) {
          if (!r.error && r.rate_date) {
            const n = parseDateNum(r.rate_date);
            if (n > maxNum) { maxNum = n; maxDate = r.rate_date; }
          }
        }

        const round: RaceRound = { id, startedAt: iso, results };

        // ── First round ever: establish baseline, no detections ──
        if (!state.priorDate) {
          set({
            rounds: [round, ...state.rounds].slice(0, 20),
            roundCounter: id,
            priorDate: maxDate,
            latestDate: maxDate,
            detections: {},
          });
          return;
        }

        const priorNum = parseDateNum(state.priorDate);

        // ── Still on baseline date: no publication yet ──
        if (maxNum <= priorNum) {
          set({
            rounds: [round, ...state.rounds].slice(0, 20),
            roundCounter: id,
            // keep priorDate, latestDate, detections unchanged
          });
          return;
        }

        // ── Date advanced beyond baseline: record who shows the new date ──
        const dateChanged = maxDate !== state.latestDate;
        const dets: Record<string, SourceDetection> =
          dateChanged ? {} : { ...state.detections };

        for (const r of results) {
          if (!r.error && r.rate_date === maxDate && !dets[r.source_id]) {
            dets[r.source_id] = {
              source_id: r.source_id,
              name: r.name,
              firstSeenAt: iso,
              latency_ms: r.latency_ms,
            };
          }
        }

        set({
          rounds: [round, ...state.rounds].slice(0, 20),
          roundCounter: id,
          latestDate: maxDate,
          detections: dets,
        });
      },

      clearHistory: () =>
        set({ rounds: [], roundCounter: 0, priorDate: '', latestDate: '', detections: {} }),
    }),
    { name: 'cbr-race-v1' }
  )
);
