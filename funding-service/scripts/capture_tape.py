"""Скачивает дневную ленту сделок TQBR по списку тикеров в компактный CSV.

Формат строки: tradeno,hhmmss,price,qty_lots,side
Нужен только для офлайн-проверки детектора роботов, в репозиторий не едет.
"""
import concurrent.futures as cf
import json
import sys
import urllib.request

BASE = ("https://iss.moex.com/iss/engines/stock/markets/shares/boards/TQBR"
        "/securities/{sec}/trades.json"
        "?iss.meta=off&iss.only=trades&limit=5000&start={start}"
        "&trades.columns=TRADENO,TRADETIME,PRICE,QUANTITY,BUYSELL")

OUT = sys.argv[1]
SECS = sys.argv[2:]


def fetch(sec):
    rows = []
    start = 0
    while True:
        url = BASE.format(sec=sec, start=start)
        with urllib.request.urlopen(url, timeout=60) as r:
            data = json.load(r)["trades"]["data"]
        if not data:
            break
        rows.extend(data)
        start += len(data)
        if len(data) < 5000:
            break
    path = f"{OUT}/{sec}.csv"
    with open(path, "w", encoding="utf-8") as f:
        for no, t, price, qty, side in rows:
            f.write(f"{no},{t},{price},{qty},{side}\n")
    return sec, len(rows)


with cf.ThreadPoolExecutor(max_workers=6) as pool:
    for sec, n in pool.map(fetch, SECS):
        print(f"{sec}: {n}", flush=True)
