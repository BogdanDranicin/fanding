import { create } from 'zustand';
import type { FundingSnapshot, WSStatus } from '../types/funding';

const MAX_HISTORY = 300;

interface FundingState {
  current: FundingSnapshot | null;
  previous: FundingSnapshot | null;
  wsStatus: WSStatus;
  history: FundingSnapshot[];
  setSnapshot: (s: FundingSnapshot) => void;
  setWSStatus: (s: WSStatus) => void;
}

export const useFundingStore = create<FundingState>((set) => ({
  current: null,
  previous: null,
  wsStatus: 'disconnected',
  history: [],

  setSnapshot: (s) =>
    set((state) => ({
      previous: state.current,
      current: s,
      history:
        state.history.length >= MAX_HISTORY
          ? [...state.history.slice(1), s]
          : [...state.history, s],
    })),

  setWSStatus: (s) => set({ wsStatus: s }),
}));
