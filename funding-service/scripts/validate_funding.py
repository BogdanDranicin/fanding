"""Сверка расчёта фандинга с биржей на сохранённых сделках.

Гоняет боевой движок (backend/cmd/simraw) по эталонным выгрузкам сырых сделок
из scripts/out и сравнивает получившийся фандинг с фактическим SWAPRATE MOEX.
Курс ЦБ и SWAPRATE тянутся из первоисточников, руками ничего вбивать не нужно.

Три режима на каждый символо-день, и все три обязаны дать один и тот же ответ:

  1. лента сделок MOEX ISS без искажений — проверка самой формулы;
  2. поток marketdata, опережающий ленту сделок на 15 минут, — грабля 28.07.2026,
     из-за которой нога фьючерса морозилась обрезанным окном;
  3. живой поток брокера при ленте ISS, отстающей на 15 минут, — то, как сервис
     работает в проде с 28.08.2026. Здесь нога обязана замёрзнуть по живому
     потоку («live») ровно в 15:30, до прихода хвоста ленты.

Запуск из каталога backend:

    python ../scripts/validate_funding.py go

Выход 0 — расхождений нет. Требует сети: ISS MOEX и cbr.ru.
"""
import json
import re
import subprocess
import sys
import urllib.request
from datetime import date, timedelta

GO = sys.argv[1]
OUT = '../scripts/out'

DAYS = ['2026-07-22', '2026-07-27', '2026-07-28', '2026-08-06', '2026-08-19']
SYMS = ['USDRUBF', 'EURRUBF']


def get(url, enc='utf-8'):
    req = urllib.request.Request(url, headers={'User-Agent': 'validate/1.0'})
    return urllib.request.urlopen(req, timeout=40).read().decode(enc)


def swaprate(sym, day):
    u = ('https://iss.moex.com/iss/history/engines/futures/markets/forts/securities/'
         '%s.json?from=%s&till=%s&iss.meta=off&iss.only=history' % (sym, day, day))
    d = json.loads(get(u))['history']
    if not d['data']:
        return None
    return dict(zip(d['columns'], d['data'][0]))['SWAPRATE']


def cbrate(sym, day):
    """Курс ЦБ, УСТАНОВЛЕННЫЙ в этот день, то есть действующий со следующего."""
    code = 'USD' if sym.startswith('USD') else 'EUR'
    d = date.fromisoformat(day)
    for step in range(1, 6):
        nxt = d + timedelta(days=step)
        t = get('https://www.cbr.ru/scripts/XML_daily.asp?date_req=%s' % nxt.strftime('%d/%m/%Y'), 'cp1251')
        # Ответ ЦБ подписан датой, на которую курс действует; выходные повторяют пятницу.
        m = re.search(r'Date="([\d.]+)"', t)
        if not m or m.group(1) != nxt.strftime('%d.%m.%Y'):
            continue
        v = re.search(r'<CharCode>%s</CharCode>.*?<VunitRate>([\d,]+)</VunitRate>' % code, t, re.S)
        if v:
            return float(v.group(1).replace(',', '.'))
    return None


def prevsettle(day, sym):
    txt = open('%s/summary_%s.txt' % (OUT, day), encoding='utf-8').read()
    block = txt.split('----- %s -----' % sym)[1]
    return float(re.search(r'PREVSETTLE\(ЦенаСпот\)=([\d.]+)', block).group(1))


def run(args):
    r = subprocess.run([GO, 'run', './cmd/simraw'] + args, capture_output=True, text=True,
                       encoding='utf-8', errors='replace')
    if r.returncode != 0:
        return None, r.stderr.strip()[:200]
    got = re.search(r'CBFunding движка = ([-\d.]+|nil)', r.stdout)
    src = re.search(r'источник ноги    = "([^"]*)" \(предварительно=(\w+)\)', r.stdout)
    return (got.group(1) if got else None, src.groups() if src else None), None


bad = 0
for day in DAYS:
    for sym in SYMS:
        path = '%s/trades_%s_%s.json' % (OUT, sym, day)
        try:
            ps = prevsettle(day, sym)
        except Exception as e:
            print('%s %s: нет эталона (%s)' % (day, sym, e))
            continue
        sw = swaprate(sym, day)
        cb = cbrate(sym, day)
        if sw is None or cb is None:
            print('%s %s: биржа/ЦБ не отдали данные' % (day, sym))
            continue
        base = ['%s' % path, sym, '%.5f' % ps, '%.4f' % cb, '%.5f' % sw]
        modes = [
            ('лента ISS', base),
            ('marketdata +900с', base + ['900']),
            ('живой поток, ISS -900с', base + ['900', '900']),
        ]
        line = '%s %-8s SWAPRATE=%+.5f  ' % (day, sym, sw)
        cells = []
        for name, args in modes:
            res, err = run(args)
            if err:
                cells.append('%s: ОШИБКА %s' % (name, err))
                bad += 1
                continue
            val, src = res
            ok = val is not None and abs(float(val) - sw) < 5e-6
            if not ok:
                bad += 1
            cells.append('%s: %s%s [%s]' % (name, val, '' if ok else ' ← РАСХОЖДЕНИЕ',
                                            src[0] if src else '?'))
        print(line + ' | '.join(cells))

print('\nрасхождений:', bad)
sys.exit(1 if bad else 0)
