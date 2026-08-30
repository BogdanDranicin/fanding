#!/usr/bin/env python3
"""
Разовый вход в Telegram под своим аккаунтом: сохраняет строку сессии для repost.py.

Запускать вручную в интерактивном терминале — нужен ввод кода из Telegram. На сервере
это делается в контейнере:
    docker compose -f docker-compose.prod.yml run --rm --entrypoint python tg-repost login.py

Сессия пишется в файл рядом с состоянием (том /data), поэтому переносить её руками
в .env не нужно: длинная строка при копировании легко рвётся, и Telethon падает
на `Incorrect padding`.

Строка сессии = полноценный доступ к аккаунту. Не коммитить и не пересылать.
"""

import asyncio
import os
from pathlib import Path

from dotenv import load_dotenv
from telethon import TelegramClient
from telethon.sessions import StringSession

from repost import session_path


def parse_proxy(url: str):
    if not url:
        return None
    from urllib.parse import urlparse

    u = urlparse(url)
    kind = {"socks5": 2, "socks4": 1, "http": 3}.get((u.scheme or "socks5").lower(), 2)
    if u.username:
        return (kind, u.hostname, u.port, True, u.username, u.password or "")
    return (kind, u.hostname, u.port)


async def main() -> None:
    load_dotenv()
    api_id = os.getenv("TG_API_ID")
    api_hash = os.getenv("TG_API_HASH")
    if not api_id or not api_hash:
        api_id = input("TG_API_ID (с my.telegram.org): ").strip()
        api_hash = input("TG_API_HASH: ").strip()

    client = TelegramClient(
        StringSession(), int(api_id), api_hash,
        proxy=parse_proxy((os.getenv("TG_PROXY_URL") or "").strip()),
    )
    try:
        await client.connect()
    except Exception as e:
        print("\n[СТОП] не удалось подключиться к Telegram (" + type(e).__name__ + "): " + str(e))
        print("       Если сервер не ходит в Telegram напрямую — задайте в .env")
        print("       TG_PROXY_URL=socks5://host:port (тот же прокси, что TELEGRAM_PROXY_URL")
        print("       у бэкенда) и запустите login.py снова.\n")
        raise SystemExit(1)

    async with client:
        me = await client.get_me()
        print("\nВход выполнен: " + (me.first_name or "") + " (id=" + str(me.id) + ")")

        session = client.session.save()
        path = Path(session_path())
        try:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(session, encoding="utf-8")
            path.chmod(0o600)
            print("\nСессия сохранена в " + str(path) + " — копировать её в .env не нужно,")
            print("repost.py возьмёт её оттуда сам. TG_SESSION в .env оставьте пустым.")
        except OSError as e:
            # Файл недоступен (запуск без тома) — тогда остаётся ручной перенос.
            print("\nНе удалось сохранить сессию в " + str(path) + ": " + str(e))
            print("Скопируйте строку ниже в .env как TG_SESSION, ОДНОЙ строкой без переносов:\n")
            print(session)

        print("\nЭта строка даёт полный доступ к аккаунту — храните как пароль.")


if __name__ == "__main__":
    asyncio.run(main())
