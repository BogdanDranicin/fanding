import { useEffect } from 'react';
import { useAlarmStore } from '../store/alarmStore';
import { alertAudioContext } from '../lib/alertSound';
import { keepAudioAlive, releaseAudio } from '../lib/audioKeepAlive';
import { notifyAlarm } from '../lib/alarmNotify';
import {
  ALARM_ARM_MS,
  ALARM_DRIFT_TOLERANCE_MS,
  ALARM_MAX_SLEEP_MS,
  alarmSleep,
  alarmTitle,
  clockOf,
  isAlarmFresh,
  mskDayStart,
  nextFireAt,
  playAlarmTone,
  scheduleAlarmTone,
  type ScheduledTone,
} from '../lib/timeAlarms';

// MIN_SLEEP_MS — нижняя граница сна: без неё планировщик, проснувшийся за
// миллисекунду до отметки, крутился бы вхолостую до неё.
const MIN_SLEEP_MS = 25;

/** «ЧЧ:ММ» МСК из момента времени. */
function mskClock(ms: number): string {
  return clockOf(Math.round((ms - mskDayStart(ms)) / 60_000));
}

/**
 * Ведёт расписание пользовательских сигналов: раз в час, раз в полчаса, в
 * заданное время. Живёт в корне приложения, а не на странице настроек, — сигнал
 * должен звонить с любой открытой страницы.
 *
 * Как это выживает в свёрнутом окне — три независимых слоя, и нужны все три:
 *
 *  1. УДЕРЖАНИЕ ВКЛАДКИ (audioKeepAlive). Пока заведён хотя бы один сигнал,
 *     страница проигрывает неслышимый тон. Вкладку, которая играет звук, Chrome
 *     не замораживает и не душит её таймеры, а её аудиоконтекст не
 *     останавливает. Без этого слоя два остальных бесполезны: у замороженной
 *     страницы не выполняется ни один таймер, ставить звук в очередь некому.
 *  2. ЗАРАНЕЕ ПОСТАВЛЕННЫЙ ЗВУК. Ноты назначаются на будущий момент по часам
 *     аудиопотока за десять минут до отметки, поэтому даже разбуженный с
 *     опозданием таймер уже ничего не портит: звук к тому времени в очереди.
 *  3. СВЕРКА СНОСА. На каждом такте поставленный звук сверяется со стенными
 *     часами. Если часы аудиопотока всё-таки стояли (машина уснула, браузер
 *     всё же приостановил контекст), постановка снимается и делается заново —
 *     иначе сигнал прозвучал бы ровно на длительность сна позже.
 */
export function useTimeAlarms(): void {
  const alarms = useAlarmStore((s) => s.alarms);
  const enabled = useAlarmStore((s) => s.enabled);
  const volume = useAlarmStore((s) => s.volume);
  const pushFired = useAlarmStore((s) => s.pushFired);

  // Удержание вкладки заводится, пока есть что звонить, и снимается, когда
  // расписание опустело или сигналы выключили: держать вкладку живой просто так
  // невежливо по отношению к батарее.
  const hasAlarms = enabled && alarms.some((a) => a.enabled);
  useEffect(() => {
    if (!hasAlarms) return;
    keepAudioAlive(alertAudioContext(), 'time-alarms');
    return () => releaseAudio('time-alarms');
  }, [hasAlarms]);

  useEffect(() => {
    if (!enabled) return;

    // Правка расписания пересобирает планировщик целиком: у изменённого сигнала
    // старое время срабатывания уже ничего не значит, а угадывать, что именно
    // поменялось, дороже, чем просто посчитать заново.
    const due = new Map<string, number>();
    const armed = new Map<string, ScheduledTone>();
    let timer = 0;

    const tick = () => {
      const now = Date.now();
      let sleep = ALARM_MAX_SLEEP_MS;

      for (const a of alarms) {
        if (!a.enabled) continue;
        let at = due.get(a.id) ?? nextFireAt(a, now);

        if (at !== 0 && now >= at) {
          const queued = armed.get(a.id);
          armed.delete(a.id);
          if (isAlarmFresh(at, now)) {
            // Звук в этот момент уже звучит из очереди WebAudio; проигрываем
            // сами только то, что поставить заранее не успели, — например
            // сигнал, заведённый за секунды до собственной отметки.
            if (!queued) playAlarmTone(a.tone, volume);
            const mark = mskClock(at + a.leadSec * 1000);
            pushFired({ key: `${a.id}:${at}`, title: alarmTitle(a), mark, ahead: a.leadSec > 0 });
            // Уведомление системы — второй канал на случай, когда окно свёрнуто
            // и плашку на странице никто не видит. Звук им не заменяется.
            notifyAlarm(alarmTitle(a), mark, a.leadSec > 0);
          } else {
            // Вкладка пролежала ночь в спящем ноутбуке: отметка давно прошла, и
            // напоминать о ней — не напоминание, а шум. Заодно снимаем звук,
            // проспавший вместе с машиной: у остановленного аудиоконтекста часы
            // стоят, и после пробуждения он догнал бы пользователя из прошлого.
            queued?.cancel();
          }
          at = nextFireAt(a, Math.max(at, now));
        }

        due.set(a.id, at);

        // Поставленный звук, чьи часы разошлись со стенными, надо переставить:
        // расхождение означает, что аудиоконтекст стоял, и нота уехала ровно на
        // длительность остановки.
        const queued = armed.get(a.id);
        if (queued && Math.abs(queued.driftMs(now)) > ALARM_DRIFT_TOLERANCE_MS) {
          queued.cancel();
          armed.delete(a.id);
        }

        if (at !== 0 && !armed.has(a.id) && at - now <= ALARM_ARM_MS) {
          armed.set(a.id, scheduleAlarmTone(a.tone, volume, at));
        }
        sleep = Math.min(sleep, alarmSleep(at, armed.has(a.id), now));
      }

      timer = window.setTimeout(tick, Math.max(MIN_SLEEP_MS, sleep));
    };

    // Возвращение к вкладке — повод пересчитать всё сразу: пока её не смотрели,
    // таймер мог просыпаться раз в минуту (а замороженная вкладка не
    // просыпалась вовсе), и плашка сигнала висит в долгу. Событий несколько,
    // потому что путей возвращения несколько: переключение вкладки, фокус окна,
    // выход из bfcache и разморозка страницы.
    const wake = () => {
      clearTimeout(timer);
      tick();
    };

    tick();
    document.addEventListener('visibilitychange', wake);
    window.addEventListener('focus', wake);
    window.addEventListener('pageshow', wake);
    document.addEventListener('resume', wake);
    return () => {
      clearTimeout(timer);
      document.removeEventListener('visibilitychange', wake);
      window.removeEventListener('focus', wake);
      window.removeEventListener('pageshow', wake);
      document.removeEventListener('resume', wake);
      for (const tone of armed.values()) tone.cancel();
    };
    // Громкость в зависимостях наравне с расписанием: пересчёт от неё ничего не
    // теряет — ближайшие отметки считаются заново от текущего момента.
  }, [alarms, enabled, volume, pushFired]);
}
