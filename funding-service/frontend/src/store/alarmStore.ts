import { create } from 'zustand';
import {
  isAlarmsEnabled,
  getAlarmVolume,
  loadAlarms,
  saveAlarms,
  setAlarmVolume,
  setAlarmsEnabled,
  type TimeAlarm,
} from '../lib/timeAlarms';

/** Сработавший сигнал — строка всплывающего уведомления. */
export interface FiredAlarm {
  /** Свой ключ, а не id сигнала: один и тот же сигнал звонит много раз за день. */
  key: string;
  title: string;
  /** Отметка, о которой сигнал предупреждает, «ЧЧ:ММ» МСК. */
  mark: string;
  /** Сигнал прозвучал заранее — в подписи это надо оговорить. */
  ahead: boolean;
}

interface AlarmState {
  alarms: TimeAlarm[];
  enabled: boolean;
  volume: number;
  fired: FiredAlarm[];
  setEnabled: (on: boolean) => void;
  setVolume: (v: number) => void;
  addAlarm: (a: TimeAlarm) => void;
  updateAlarm: (id: string, patch: Partial<TimeAlarm>) => void;
  removeAlarm: (id: string) => void;
  pushFired: (f: FiredAlarm) => void;
  dismissFired: (key: string) => void;
}

// Расписание сигналов — общее состояние всего приложения: заводят их на странице
// настроек, а звонят они на любой открытой странице, поэтому владеть списком
// не может ни та, ни другая.
//
// Каждое изменение сразу пишется в localStorage: расписание должно пережить
// перезагрузку страницы, а иного хранилища у него нет.
export const useAlarmStore = create<AlarmState>((set, get) => ({
  alarms: loadAlarms(),
  enabled: isAlarmsEnabled(),
  volume: getAlarmVolume(),
  fired: [],

  setEnabled: (on) => {
    setAlarmsEnabled(on);
    set({ enabled: on });
  },

  setVolume: (v) => {
    setAlarmVolume(v);
    set({ volume: v });
  },

  addAlarm: (a) => {
    const next = [...get().alarms, a];
    saveAlarms(next);
    set({ alarms: next });
  },

  updateAlarm: (id, patch) => {
    const next = get().alarms.map((a) => (a.id === id ? { ...a, ...patch } : a));
    saveAlarms(next);
    set({ alarms: next });
  },

  removeAlarm: (id) => {
    const next = get().alarms.filter((a) => a.id !== id);
    saveAlarms(next);
    set({ alarms: next });
  },

  // Всплывающих уведомлений держим не больше трёх: пропущенная за ночь пачка
  // сигналов не должна закрыть собой страницу.
  pushFired: (f) => set({ fired: [...get().fired, f].slice(-3) }),

  dismissFired: (key) => set({ fired: get().fired.filter((f) => f.key !== key) }),
}));
