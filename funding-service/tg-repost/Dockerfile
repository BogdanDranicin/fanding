FROM python:3.12-slim

WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# cryptg ускоряет шифрование MTProto (заметно на медиа), но это НЕобязательная
# зависимость: она на Rust, и без готового колеса под текущий Python ей нужен
# cargo. Поэтому ставим только из бинарного колеса и не валим сборку при неудаче —
# без неё Telethon работает на чистом Python, просто медленнее.
RUN pip install --no-cache-dir --only-binary=:all: cryptg \
 || echo "cryptg недоступен для этой платформы — работаем без ускорения"

COPY repost.py login.py ./

# /data — состояние (SQLite с уже перенесёнными id). Монтируется томом,
# без него при перезапуске контейнера начнётся повтор истории.
VOLUME ["/data"]
ENV STATE_DB=/data/state.db PYTHONUNBUFFERED=1

ENTRYPOINT ["python", "repost.py"]
