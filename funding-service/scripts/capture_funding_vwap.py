#!/usr/bin/env python3
"""
capture_funding_vwap.py — эталонный замер VWAP вечных фьючерсов для сверки фандинга.

ЗАЧЕМ: наш CBFunding (реконструкция в момент публикации курса ЦБ) завышает факт
MOEX SWAPRATE на ~0.0036. Формула и константы (K1=0.1%, K2=0.15%) подтверждены
официально (moex.com/a8141). Вся ошибка сидит в первом члене D — во ВЗВЕШЕННОЙ ПО
ОБЪЁМУ ЦЕНЕ безадресных сделок 10:00–15:30. Этот скрипт считает её ТОЧНО по сырым
сделкам ISS и сравнивает с нашим settl_vwap (прод-журнал) и с фактом (обратным
счётом из SWAPRATE), чтобы понять — баг у нас в движке или методика MOEX иная.

КОГДА ЗАПУСКАТЬ: в торговый день, СРАЗУ ПОСЛЕ 15:30 МСК и до вечера (ISS отдаёт
сделки только текущей торговой сессии; после закрытия дня они пропадают —
проверено, 21.07 вечером эндпоинт уже пуст). Идеально 15:35–17:00 МСК.

ЗАПУСК:  python capture_funding_vwap.py
         python capture_funding_vwap.py --date 2026-07-23   # переопределить дату

Зависимостей нет (только стандартная библиотека). Сохраняет:
  - сырые сделки:  scripts/out/trades_<SEC>_<DATE>.json
  - сводку:        scripts/out/summary_<DATE>.txt
"""

import argparse
import datetime as dt
import json
import os
import sys
import urllib.request

# Windows: stdout по умолчанию cp1251 → кириллица в консоли ломается. Форсим UTF-8.
try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

ISS = "https://iss.moex.com/iss"
PROD = "https://funding.mooo.com"

MSK = dt.timezone(dt.timedelta(hours=3))

# Официальные параметры контракта (moex.com/a8141, onepager). Для USDRUBF/EURRUBF:
K1 = 0.001    # допустимое отклонение L1 = K1 * ЦенаСпот (мёртвая зона)
K2 = 0.0015   # максимум       L2 = K2 * ЦенаСпот (кап)

# Окно расчёта D для USDRUBF/EURRUBF (методика с 24.06.2024): безадресные сделки 10:00–15:30 МСК.
WIN_LO = (10, 0)
WIN_HI = (15, 30)

SECIDS = ["USDRUBF", "EURRUBF"]

OUT_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "out")


def get_json(url, timeout=40):
    with urllib.request.urlopen(url, timeout=timeout) as r:
        return json.load(r)


def in_window(hhmm):
    """hhmm: 'HH:MM:SS' -> True если в [10:00, 15:30) МСК."""
    try:
        h, m, _ = hhmm.split(":")
        t = (int(h), int(m))
    except Exception:
        return False
    return WIN_LO <= t < WIN_HI


def fetch_all_trades(secid):
    """Форвардная пагинация как в движке: tradeno=N&next_trade=1. Дедуп по TRADENO."""
    all_rows, cols = [], None
    cursor = 0
    seen = set()
    pages = 0
    while pages < 400:
        url = (f"{ISS}/engines/futures/markets/forts/securities/{secid}/trades.json"
               f"?tradeno={cursor}&next_trade=1&iss.meta=off")
        d = get_json(url)
        t = d["trades"]
        cols = t["columns"]
        data = t["data"]
        if not data:
            break
        ci = {c: i for i, c in enumerate(cols)}
        max_no = cursor
        new = 0
        for row in data:
            no = int(row[ci["TRADENO"]])
            if no in seen:
                continue
            seen.add(no)
            all_rows.append(row)
            new += 1
            if no > max_no:
                max_no = no
        pages += 1
        if new == 0 or max_no == cursor:
            break
        cursor = max_no
    return cols, all_rows


def vwap(cols, rows, only_onbook=True, window=True):
    ci = {c: i for i, c in enumerate(cols)}
    spv = sv = 0.0
    n = 0
    for row in rows:
        price = float(row[ci["PRICE"]])
        qty = float(row[ci["QUANTITY"]])
        if price <= 0 or qty <= 0:
            continue
        off = float(row[ci.get("OFFMARKETDEAL", -1)] or 0) if "OFFMARKETDEAL" in ci else 0
        if only_onbook and off != 0:
            continue
        if window and not in_window(str(row[ci["TRADETIME"]])):
            continue
        spv += price * qty
        sv += qty
        n += 1
    return (spv / sv if sv else None), sv, n


def clamp_funding(D, L1, L2):
    inner = min(-L1, D) + max(L1, D)
    return min(L2, max(-L2, inner))


def sec_field(secid, block, field):
    url = (f"{ISS}/engines/futures/markets/forts/securities/{secid}.json?iss.meta=off")
    d = get_json(url)
    b = d[block]
    ci = {c: i for i, c in enumerate(b["columns"])}
    if not b["data"] or field not in ci:
        return None
    return b["data"][0][ci[field]]


def prod_publication():
    """Последняя строка журнала прод: наш settl_vwap / cb_funding / курс ЦБ / факт SWAPRATE."""
    try:
        d = get_json(f"{PROD}/api/v1/cb-publications")
        return d[0] if d else None
    except Exception as e:
        return {"_error": str(e)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--date", help="МСК-дата YYYY-MM-DD (по умолчанию сегодня)")
    args = ap.parse_args()

    today = args.date or dt.datetime.now(MSK).strftime("%Y-%m-%d")
    os.makedirs(OUT_DIR, exist_ok=True)

    lines = []
    def out(s=""):
        print(s)
        lines.append(s)

    out(f"=== Эталонный замер VWAP фандинга — {today} (МСК) ===")
    out(f"Окно D: {WIN_LO[0]:02d}:{WIN_LO[1]:02d}–{WIN_HI[0]:02d}:{WIN_HI[1]:02d}, только безадресные (OFFMARKETDEAL==0)")
    out(f"K1={K1}  K2={K2}")
    out("")

    pub = prod_publication()
    pub_rate = {"USDRUBF": None, "EURRUBF": None}
    pub_settl = {"USDRUBF": None, "EURRUBF": None}
    pub_cbf = {"USDRUBF": None, "EURRUBF": None}
    pub_moex = {"USDRUBF": None, "EURRUBF": None}
    if pub and "_error" not in pub:
        pub_rate = {"USDRUBF": pub.get("usd_rate"), "EURRUBF": pub.get("eur_rate")}
        pub_settl = {"USDRUBF": pub.get("settl_vwap_usd"), "EURRUBF": pub.get("settl_vwap_eur")}
        pub_cbf = {"USDRUBF": pub.get("cb_funding_usd"), "EURRUBF": pub.get("cb_funding_eur")}
        pub_moex = {"USDRUBF": pub.get("moex_funding_usd"), "EURRUBF": pub.get("moex_funding_eur")}
        out(f"Прод-журнал: дата={pub.get('date','')[:10]} курс USD={pub_rate['USDRUBF']} EUR={pub_rate['EURRUBF']}")
    else:
        out(f"Прод-журнал недоступен: {pub}")
    out("")

    for secid in SECIDS:
        out(f"----- {secid} -----")
        trades_path = os.path.join(OUT_DIR, f"trades_{secid}_{today}.json")
        cols, rows = fetch_all_trades(secid)
        if rows:
            # Живые сделки есть — сохраняем (первый прогон ~15:35, пока ISS их отдаёт).
            with open(trades_path, "w", encoding="utf-8") as f:
                json.dump({"columns": cols, "data": rows}, f, ensure_ascii=False)
        elif os.path.exists(trades_path):
            # Повторный прогон вечером: ISS день уже очистил — берём сохранённое.
            out("  (живых сделок нет — использую сохранённые из первого прогона)")
            saved = json.load(open(trades_path, encoding="utf-8"))
            cols, rows = saved["columns"], saved["data"]
        else:
            out("  СДЕЛОК НЕТ и нет сохранённого файла. Запусти между 15:30 и вечером МСК,")
            out("  пока ISS отдаёт сделки текущей сессии. Пропускаю.")
            out("")
            continue

        v_win_on, vol_win, n_win = vwap(cols, rows, only_onbook=True, window=True)
        v_win_all, _, n_win_all = vwap(cols, rows, only_onbook=False, window=True)
        v_day_on, vol_day, n_day = vwap(cols, rows, only_onbook=True, window=False)

        out(f"  сделок всего: {len(rows)} | в окне безадресных: {n_win} (объём {vol_win:.0f})")
        out(f"  VWAP 10:00–15:30 безадресные (ЭТАЛОН D-цены) = {fmt(v_win_on)}")
        out(f"  VWAP 10:00–15:30 включая адресные            = {fmt(v_win_all)}  (для контроля фильтра)")
        out(f"  VWAP весь день безадресные                    = {fmt(v_day_on)}  (для контроля окна)")

        prev_settle = tofloat(sec_field(secid, "securities", "PREVSETTLEPRICE"))
        cur_swap = tofloat(sec_field(secid, "marketdata", "SWAPRATE"))
        cur_settle = tofloat(sec_field(secid, "marketdata", "SETTLEPRICE"))
        out(f"  MOEX: PREVSETTLE(ЦенаСпот)={prev_settle} | SWAPRATE(факт, если вышел)={cur_swap} | SETTLEPRICE={cur_settle}")

        rate = tofloat(pub_rate[secid])
        base = prev_settle  # спека: ЦенаСпот = предыдущая расчётная цена ВФ
        if v_win_on is not None and rate and base:
            L1 = K1 * base
            L2 = K2 * base
            D = v_win_on - rate
            f_ref = clamp_funding(D, L1, L2)
            out(f"  --> D = {v_win_on:.5f} − {rate} = {D:.5f}; L1={L1:.5f} L2={L2:.5f}")
            out(f"  --> Фандинг по ЭТАЛОННОМУ VWAP        = {f_ref:.6f}")
            if cur_swap:
                out(f"  --> факт MOEX SWAPRATE                 = {cur_swap:.6f}  (Δ={f_ref-cur_swap:+.6f})")
        else:
            out("  (курс ЦБ ещё не опубликован — фандинг посчитаем, когда выйдет; сырые сделки уже сохранены)")

        # Сверка нашего движка
        our_v = tofloat(pub_settl[secid])
        if our_v is not None and v_win_on is not None:
            out(f"  СВЕРКА ДВИЖКА: наш settl_vwap={our_v:.5f} vs эталон={v_win_on:.5f} -> Δ={our_v - v_win_on:+.5f}")
            if abs(our_v - v_win_on) > 0.002:
                out("     ⚠️  РАСХОЖДЕНИЕ > 0.002 — баг в нашем VWAP (вероятно откат на ΔVOLTODAY / неполный фид).")
            else:
                out("     ✅ наш VWAP совпал с эталоном — движок считает верно; тогда смотреть на методику MOEX.")
        if pub_cbf[secid] is not None:
            out(f"  наш CBFunding (прод)={pub_cbf[secid]} | наш moex_funding(прод)={pub_moex[secid]}")
        out("")

    with open(os.path.join(OUT_DIR, f"summary_{today}.txt"), "w", encoding="utf-8") as f:
        f.write("\n".join(lines))
    out(f"Сводка сохранена: {os.path.join(OUT_DIR, f'summary_{today}.txt')}")


def fmt(v):
    return f"{v:.5f}" if isinstance(v, (int, float)) else "—"


def tofloat(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


if __name__ == "__main__":
    main()
