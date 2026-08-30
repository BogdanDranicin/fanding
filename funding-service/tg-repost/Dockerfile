FROM python:3.12-slim

# cryptg (ускорение шифрования MTProto) собирается из исходников — нужен компилятор.
RUN apt-get update \
 && apt-get install -y --no-install-recommends build-essential \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt \
 && apt-get purge -y build-essential && apt-get autoremove -y

COPY repost.py login.py ./

# /data — состояние (SQLite с уже перенесёнными id). Монтируется томом,
# без него при перезапуске контейнера начнётся повтор истории.
VOLUME ["/data"]
ENV STATE_DB=/data/state.db PYTHONUNBUFFERED=1

ENTRYPOINT ["python", "repost.py"]
