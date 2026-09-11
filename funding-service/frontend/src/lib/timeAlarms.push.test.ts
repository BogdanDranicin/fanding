import { describe, expect, it } from 'vitest';
import { alarmWakeups, makeAlarm, mskClock, nextFireAt } from './timeAlarms';

// Расписание для push — это то же расписание, только посчитанное вперёд и
// отданное серверу. Здесь проверяется ровно это: что посчитано вперёд верно и
// что подпись уведомления совпадает с тем, что показала бы сама страница.

function msk(iso: string): number {
  return Date.parse(`${iso}+03:00`);
}

describe('отметки для push', () => {
  it('идут по порядку и совпадают с расчётом самой страницы', () => {
    const hourly = makeAlarm({ everyMin: 60, label: 'Час' });
    const from = msk('2026-09-11T10:17:00');

    const got = alarmWakeups([hourly], from, 3 * 60 * 60 * 1000);

    expect(got.map((w) => w.at)).toEqual([
      msk('2026-09-11T11:00:00'),
      msk('2026-09-11T12:00:00'),
      msk('2026-09-11T13:00:00'),
    ]);
    // Первая отметка обязана совпасть с той, которую планировщик страницы
    // поставит себе сам: разойдись они — push звонил бы не тогда, когда вкладка.
    expect(got[0].at).toBe(nextFireAt(hourly, from));
    expect(got[0].title).toBe('Час');
    expect(got[0].body).toBe('11:00 МСК');
    expect(got[0].tag).toBe('time-alarm:Час');
  });

  it('учитывают предупреждение заранее: звонок раньше отметки, подпись — про отметку', () => {
    const a = makeAlarm({ everyMin: 60, leadSec: 30, label: 'Перед часом' });
    const from = msk('2026-09-11T10:00:00');

    const [first] = alarmWakeups([a], from, 2 * 60 * 60 * 1000);

    expect(first.at).toBe(msk('2026-09-11T10:59:30'));
    expect(first.body).toBe('Скоро 11:00 МСК');
  });

  it('не берут выключенные сигналы', () => {
    const on = makeAlarm({ everyMin: 60, label: 'Живой' });
    const off = makeAlarm({ everyMin: 30, label: 'Выключённый', enabled: false });

    const got = alarmWakeups([on, off], msk('2026-09-11T10:00:00'), 60 * 60 * 1000);

    expect(got.every((w) => w.title === 'Живой')).toBe(true);
  });

  it('сводят несколько сигналов в один список по времени', () => {
    const hour = makeAlarm({ everyMin: 60, label: 'Час' });
    const half = makeAlarm({ everyMin: 30, label: 'Полчаса' });

    const got = alarmWakeups([hour, half], msk('2026-09-11T10:05:00'), 70 * 60 * 1000);

    expect(got.map((w) => `${mskClock(w.at)} ${w.title}`)).toEqual([
      '10:30 Полчаса',
      '11:00 Час',
      '11:00 Полчаса',
    ]);
  });

  it('обрезаются потолком: частый сигнал не должен занять весь список', () => {
    const minute = makeAlarm({ everyMin: 1, label: 'Минута' });

    const got = alarmWakeups([minute], msk('2026-09-11T10:00:00'), 12 * 60 * 60 * 1000, 5);

    expect(got).toHaveLength(5);
    expect(got[4].at).toBe(msk('2026-09-11T10:05:00'));
  });

  it('пропускают выходные у сигнала «по будням»', () => {
    // 12.09.2026 — суббота: следующая отметка обязана уехать на понедельник.
    const workday = makeAlarm({ kind: 'at', atMin: 10 * 60, weekdaysOnly: true, label: 'Открытие' });

    const got = alarmWakeups([workday], msk('2026-09-11T12:00:00'), 4 * 24 * 60 * 60 * 1000);

    // Суббота и воскресенье пропущены целиком, будни идут подряд.
    expect(got.map((w) => w.at)).toEqual([
      msk('2026-09-14T10:00:00'),
      msk('2026-09-15T10:00:00'),
    ]);
  });

  it('пустое расписание — пустой список, а не мусор', () => {
    expect(alarmWakeups([], Date.now())).toEqual([]);
  });
});
