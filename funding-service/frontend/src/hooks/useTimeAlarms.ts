import { useEffect, useRef } from 'react';
import { useAlarmStore } from '../store/alarmStore';
import {
  alarmTitle,
  clockOf,
  mskDayStart,
  nextFireAt,
  playAlarmTone,
} from '../lib/timeAlarms';

// TICK_MS — шаг проверки расписания. Секунды не хватило бы только на сигнал,
// который обязан попасть в точную миллисекунду; таких нет, а полсекунды дают
// запас на подтормаживание вкладки и почти ничего не стоят.
const TICK_MS = 500;

// STALE_MS — насколько просроченный сигнал ещё имеет смысл проиграть. Вкладка,
// пролежавшая ночь в спящем ноутбуке, не должна проснуться пачкой сигналов о
// давно прошедших отметках: это не напоминание, а шум.
const STALE_MS = 60_000;

/** «ЧЧ:ММ» МСК из момента времени. */
function mskClock(ms: number): string {
  return clockOf(Math.round((ms - mskDayStart(ms)) / 60_000));
}

/**
 * Ведёт расписание пользовательских сигналов: раз в час, раз в полчаса, в
 * заданное время. Живёт в корне приложения, а не на странице настроек, — сигнал
 * должен звонить с любой открытой страницы.
 */
export function useTimeAlarms(): void {
  const alarms = useAlarmStore((s) => s.alarms);
  const enabled = useAlarmStore((s) => s.enabled);
  const volume = useAlarmStore((s) => s.volume);
  const pushFired = useAlarmStore((s) => s.pushFired);

  // Ближайшее срабатывание каждого сигнала. В ref, а не в состоянии: расписание
  // проверяется дважды в секунду и перерисовывать из-за него нечего.
  const dueRef = useRef<Map<string, number>>(new Map());

  useEffect(() => {
    if (!enabled) {
      dueRef.current.clear();
      return;
    }

    // Правка расписания пересчитывает его целиком: у изменённого сигнала старое
    // время срабатывания уже ничего не значит, а угадывать, что именно
    // поменялось, дороже, чем просто посчитать заново.
    const due = dueRef.current;
    due.clear();

    const tick = () => {
      const now = Date.now();
      for (const a of alarms) {
        if (!a.enabled) continue;
        let at = due.get(a.id);
        if (at === undefined) {
          at = nextFireAt(a, now);
          due.set(a.id, at);
          continue;
        }
        if (at === 0 || now < at) continue;

        // Пропущенное срабатывание молча проглатываем — но расписание всё равно
        // двигаем вперёд, иначе сигнал застрянет на прошедшей отметке.
        if (now - at <= STALE_MS) {
          playAlarmTone(a.tone, volume);
          pushFired({
            key: `${a.id}:${at}`,
            title: alarmTitle(a),
            mark: mskClock(at + a.leadSec * 1000),
            ahead: a.leadSec > 0,
          });
        }
        due.set(a.id, nextFireAt(a, Math.max(at, now)));
      }
    };

    tick();
    const id = setInterval(tick, TICK_MS);
    return () => clearInterval(id);
    // Громкость в зависимостях наравне с расписанием: пересчёт от неё ничего не
    // теряет — ближайшие отметки считаются заново от текущего момента.
  }, [alarms, enabled, volume, pushFired]);
}
