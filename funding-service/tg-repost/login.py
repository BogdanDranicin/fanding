#!/usr/bin/env python3
"""
Разовый вход в Telegram под своим аккаунтом: печатает строку сессии для TG_SESSION.

Запускать ТОЛЬКО локально, вручную, в обычном терминале — нужен ввод кода из Telegram.
На сервере интерактивного ввода нет, поэтому туда уезжает уже готовая строка сессии.

Строка сессии = полноценный доступ к аккаунту. Не коммитить, не пересылать,
хранить только в .env (он в .gitignore).
"""

import asyncio
import os

from dotenv import load_dotenv
from telethon import TelegramClient
from telethon.sessions import StringSession


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
    async with client:
        me = await client.get_me()
        print("\nВход выполнен: " + (me.first_name or "") + " (id=" + str(me.id) + ")")
        print("\nСкопируйте строку ниже в .env как TG_SESSION (одной строкой):\n")
        print(client.session.save())
        print("\nЭта строка даёт полный доступ к аккаунту — храните как пароль.")


if __name__ == "__main__":
    asyncio.run(main())
