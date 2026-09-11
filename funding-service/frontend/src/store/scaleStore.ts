import { create } from 'zustand';
import {
  DEFAULT_SCALE,
  loadFundingScale,
  normalizeScale,
  saveFundingScale,
  type FundingScale,
} from '../lib/fundingScale';

interface ScaleState {
  scale: FundingScale;
  setScale: (patch: Partial<FundingScale>) => void;
  resetScale: () => void;
}

// Пороги подсветки нужны и таблице, и отдельному окну CB funding, а меняются они
// на странице настроек — общим состоянием владеет store, а не какая-то из страниц.
export const useScaleStore = create<ScaleState>((set, get) => ({
  scale: loadFundingScale(),

  setScale: (patch) => {
    const next = normalizeScale({ ...get().scale, ...patch });
    saveFundingScale(next);
    set({ scale: next });
  },

  resetScale: () => {
    saveFundingScale(DEFAULT_SCALE);
    set({ scale: DEFAULT_SCALE });
  },
}));
