#!/usr/bin/env python3
"""
Перепост сообщений из закрытого чата в закрытый канал от имени пользователя (userbot).

Зачем userbot, а не bot API: бота нельзя добавить в чат, куда нет доступа, поэтому
читаем источник обычным аккаунтом через MTProto (Telethon).

Доставка бывает двух видов, переключается FORWARD:
  пересылка (FORWARD=true, по умолчанию) — Telegram сам ставит «Переслано из …»,
      сохраняет автора, разметку, альбомы и медиа, ничего не качая;
  копирование (FORWARD=false) — сообщение собирается заново, автор приписывается
      строкой. Нужно, когда источник запрещает пересылку (защита контента).
Оба пути живут рядом: если пересылку запретят, достаточно поставить FORWARD=false.

ГЛАВНОЕ ПРАВИЛО МОДУЛЯ: сообщение не должно уйти в чат-источник или в любой другой
чат, кроме подтверждённой цели. Поэтому проверки цели продублированы на трёх уровнях:
  1. preflight   — разбор и сверка обоих чатов до единой отправки;
  2. binding     — пара (источник, цель) фиксируется в состоянии, смена требует флага;
  3. send_guard  — каждая отправка ещё раз сверяет получателя с подтверждённым.
Любое расхождение = аварийный выход, а не «попробуем дальше».
"""

from __future__ import annotations

import argparse
import asyncio
import copy
import datetime as dt
import logging
import os
import sqlite3
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

from dotenv import load_dotenv
from telethon import TelegramClient, events, utils
from telethon.errors import ChatForwardsRestrictedError, FloodWaitError
from telethon.sessions import StringSession
from telethon.tl.types import (
    Channel,
    Chat,
    MessageEntityBold,
    MessageMediaWebPage,
    MessageService,
    User,
)

log = logging.getLogger("repost")

EXIT_GUARD = 2  # отдельный код выхода: сработал предохранитель, а не обычная ошибка


# --------------------------------------------------------------------------- config


def die(msg: str, code: int = EXIT_GUARD) -> None:
    print("\n[СТОП] " + msg + "\n", file=sys.stderr)
    sys.exit(code)


def env_bool(name: str, default: bool) -> bool:
    raw = os.getenv(name)
    if raw is None or raw.strip() == "":
        return default
    return raw.strip().lower() in ("1", "true", "yes", "y", "on", "да")


def env_int(name: str, default: int) -> int:
    raw = os.getenv(name)
    if raw is None or raw.strip() == "":
        return default
    return int(raw)


@dataclass
class Config:
    api_id: int
    api_hash: str
    session: str
    src_raw: str
    dst_raw: str
    dst_title_expected: str
    mode: str            # history | live | both
    dry_run: bool
    allow_retarget: bool
    send_canary: bool
    forward: bool
    show_author: bool
    author_template: str
    include_media: bool
    include_documents: bool
    albums: bool
    history_limit: int
    history_min_id: int
    history_since: dt.datetime | None
    history_delay_ms: int
    live_delay_ms: int
    state_db: str
    proxy_url: str

    @staticmethod
    def load(require_chats: bool = True) -> "Config":
        # --dialogs запускают как раз для того, чтобы УЗНАТЬ id чатов: требовать их
        # заранее нельзя, иначе список чатов не посмотреть.
        need = ["TG_API_ID", "TG_API_HASH"]
        if require_chats:
            need += ["SRC_CHAT", "DST_CHAT"]
        missing = [k for k in need if not os.getenv(k)]
        if missing:
            die("не заданы обязательные переменные: " + ", ".join(missing) + " (см. .env.example)")

        session = read_session()
        if not session:
            die("нет строки сессии: ни TG_SESSION в .env, ни файла " + session_path() + "\n"
                "       Выполните login.py — он сохранит сессию в этот файл сам.")

        since_raw = (os.getenv("HISTORY_SINCE") or "").strip()
        since = None
        if since_raw:
            try:
                since = dt.datetime.fromisoformat(since_raw).replace(tzinfo=dt.timezone.utc)
            except ValueError:
                die("HISTORY_SINCE='" + since_raw + "' — ожидается дата вида 2026-01-31 или 2026-01-31T10:00")

        mode = (os.getenv("MODE") or "both").strip().lower()
        if mode not in ("history", "live", "both"):
            die("MODE='" + mode + "' — допустимо history | live | both")

        return Config(
            api_id=env_int("TG_API_ID", 0),
            api_hash=os.environ["TG_API_HASH"].strip(),
            session=session,
            src_raw=(os.getenv("SRC_CHAT") or "").strip(),
            dst_raw=(os.getenv("DST_CHAT") or "").strip(),
            dst_title_expected=(os.getenv("DST_TITLE_EXPECTED") or "").strip(),
            mode=mode,
            # Ключевой дефолт: без явного DRY_RUN=false скрипт ничего не отправляет.
            dry_run=env_bool("DRY_RUN", True),
            allow_retarget=env_bool("ALLOW_RETARGET", False),
            send_canary=env_bool("SEND_CANARY", True),
            # Пересылка вместо копии. Если Telegram или чат-источник её запретит —
            # FORWARD=false возвращает прежнее поведение, остальной код не меняется.
            forward=env_bool("FORWARD", True),
            show_author=env_bool("SHOW_AUTHOR", True),
            # Подпись автора первой строкой; {name} подставляется при отправке.
            # \n в .env приезжает двумя символами — разворачиваем в перевод строки.
            author_template=(os.getenv("AUTHOR_TEMPLATE") or "{name}:\n").replace("\\n", "\n"),
            include_media=env_bool("INCLUDE_MEDIA", True),
            include_documents=env_bool("INCLUDE_DOCUMENTS", True),
            albums=env_bool("ALBUMS", True),
            history_limit=env_int("HISTORY_LIMIT", 0),
            history_min_id=env_int("HISTORY_MIN_ID", 0),
            history_since=since,
            history_delay_ms=env_int("HISTORY_DELAY_MS", 3000),
            live_delay_ms=env_int("LIVE_DELAY_MS", 500),
            state_db=os.getenv("STATE_DB") or "./state.db",
            proxy_url=(os.getenv("TG_PROXY_URL") or "").strip(),
        )


def utf16len(s: str) -> int:
    """Длина в кодовых единицах UTF-16 — именно в них Telegram считает смещения."""
    return len(s.encode("utf-16-le")) // 2


def shift_entities(entities, delta: int):
    """Сдвигает разметку исходного сообщения на длину приписанной подписи автора.

    Без сдвига жирный/курсив/ссылки уезжают на начало текста. Копируем объекты,
    чтобы не портить сообщение, которое Telethon мог закэшировать.
    """
    out = []
    for e in entities or []:
        e = copy.copy(e)
        e.offset += delta
        out.append(e)
    return out


async def author_of(msg) -> str:
    """Кто написал сообщение: пользователь, канал или подпись автора поста."""
    try:
        sender = await msg.get_sender()
    except Exception:
        sender = None
    if sender is not None:
        name = utils.get_display_name(sender)
        if name:
            return name
    # У постов в канале отправителя как такового нет — есть подпись, если включена.
    return getattr(msg, "post_author", None) or ""


def session_path() -> str:
    """Файл со строкой сессии — рядом с состоянием, то есть в томе /data."""
    explicit = (os.getenv("SESSION_FILE") or "").strip()
    if explicit:
        return explicit
    return str(Path(os.getenv("STATE_DB") or "./state.db").parent / "session.txt")


def read_session() -> str:
    """TG_SESSION из окружения, иначе файл в /data.

    Файл надёжнее: строка сессии длиной ~350 символов при копировании в .env
    легко рвётся переносом, и Telethon падает на `Incorrect padding`.
    """
    env = (os.getenv("TG_SESSION") or "").strip()
    if env:
        return env
    path = Path(session_path())
    if path.exists():
        return path.read_text(encoding="utf-8").strip()
    return ""


def check_session(session: str) -> None:
    """Ранняя проверка строки сессии: понятная ошибка вместо трейсбека base64."""
    try:
        StringSession(session)
    except Exception as e:
        die("строка сессии повреждена (" + type(e).__name__ + ": " + str(e) + ").\n"
            "       Длина: " + str(len(session)) + " символов, ожидается около 350 в одну строку.\n"
            "       Чаще всего она рвётся при вставке в .env. Проще всего перевыпустить:\n"
            "       login.py сохранит её в " + session_path() + " сам, тогда TG_SESSION\n"
            "       в .env можно оставить пустым.", code=1)


def parse_proxy(url: str):
    """socks5://user:pass@host:port -> кортеж для Telethon (python-socks)."""
    if not url:
        return None
    from urllib.parse import urlparse

    u = urlparse(url)
    scheme = (u.scheme or "socks5").lower()
    kind = {"socks5": 2, "socks4": 1, "http": 3}.get(scheme)
    if kind is None:
        die("TG_PROXY_URL: неизвестная схема '" + scheme + "' (socks5 | socks4 | http)")
    if not u.hostname or not u.port:
        die("TG_PROXY_URL: нужен host:port")
    if u.username:
        return (kind, u.hostname, u.port, True, u.username, u.password or "")
    return (kind, u.hostname, u.port)


# ---------------------------------------------------------------------------- peers


def parse_peer(raw: str):
    """Принимает -1001234567890, @name, https://t.me/c/1234567890/5, t.me/name."""
    s = raw.strip()
    for prefix in ("https://", "http://"):
        if s.startswith(prefix):
            s = s[len(prefix):]
    if s.startswith("t.me/"):
        rest = s[len("t.me/"):].strip("/")
        if rest.startswith("c/"):
            parts = rest.split("/")
            digits = parts[1] if len(parts) > 1 else ""
            if not digits.isdigit():
                die("не разобрал ссылку на чат: " + raw)
            return int("-100" + digits)
        return "@" + rest.split("/")[0].lstrip("@")
    if s.lstrip("-").isdigit():
        return int(s)
    return "@" + s.lstrip("@")


def peer_key(entity) -> int:
    """Канонический id (-100… для каналов) — по нему и сравниваем чаты."""
    return utils.get_peer_id(entity)


def kind_of(entity) -> str:
    if isinstance(entity, User):
        return "личный чат"
    if isinstance(entity, Chat):
        return "группа"
    if isinstance(entity, Channel):
        return "канал" if entity.broadcast else "супергруппа"
    return type(entity).__name__


def describe(entity) -> str:
    title = getattr(entity, "title", None) or utils.get_display_name(entity) or "?"
    username = getattr(entity, "username", None)
    uname = " @" + username if username else " (без username, приватный)"
    return "«" + title + "»" + uname + " — " + kind_of(entity) + ", id=" + str(peer_key(entity))


# ---------------------------------------------------------------------------- state


class State:
    def __init__(self, path: str):
        parent = Path(path).parent
        if str(parent):
            parent.mkdir(parents=True, exist_ok=True)
        self.db = sqlite3.connect(path)
        self.db.execute("PRAGMA journal_mode=WAL")
        self.db.executescript(
            """
            CREATE TABLE IF NOT EXISTS posted (
                src_id     INTEGER NOT NULL,
                src_msg_id INTEGER NOT NULL,
                dst_id     INTEGER NOT NULL,
                dst_msg_id INTEGER,
                ts         TEXT    NOT NULL,
                PRIMARY KEY (src_id, src_msg_id)
            );
            CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
            """
        )
        self.db.commit()

    def get(self, k: str):
        row = self.db.execute("SELECT v FROM meta WHERE k=?", (k,)).fetchone()
        return row[0] if row else None

    def set(self, k: str, v: str) -> None:
        self.db.execute(
            "INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v",
            (k, v),
        )
        self.db.commit()

    def already_posted(self, src_id: int, msg_id: int) -> bool:
        return self.db.execute(
            "SELECT 1 FROM posted WHERE src_id=? AND src_msg_id=?", (src_id, msg_id)
        ).fetchone() is not None

    def mark(self, src_id: int, msg_id: int, dst_id: int, dst_msg_id) -> None:
        self.db.execute(
            "INSERT OR REPLACE INTO posted(src_id, src_msg_id, dst_id, dst_msg_id, ts) "
            "VALUES(?,?,?,?,?)",
            (src_id, msg_id, dst_id, dst_msg_id, dt.datetime.now(dt.timezone.utc).isoformat()),
        )
        self.db.commit()

    def count(self, src_id: int) -> int:
        return self.db.execute(
            "SELECT COUNT(*) FROM posted WHERE src_id=?", (src_id,)
        ).fetchone()[0]


# --------------------------------------------------------------------------- guards


async def preflight(client: TelegramClient, cfg: Config, state: State):
    """Разбирает оба чата и валит запуск при любом сомнении. Возвращает (src, dst)."""
    me = await client.get_me()

    hint = "\n       Подсказка: запустите с --dialogs, чтобы увидеть точные id ваших чатов."
    try:
        src = await client.get_entity(parse_peer(cfg.src_raw))
    except Exception as e:
        die("не удалось открыть SRC_CHAT='" + cfg.src_raw + "': " + str(e) + hint)
    try:
        dst = await client.get_entity(parse_peer(cfg.dst_raw))
    except Exception as e:
        die("не удалось открыть DST_CHAT='" + cfg.dst_raw + "': " + str(e) + hint)

    src_key, dst_key = peer_key(src), peer_key(dst)

    print("=" * 72)
    print("Аккаунт  : " + utils.get_display_name(me) + " (id=" + str(peer_key(me)) + ")")
    print("ИСТОЧНИК : " + describe(src))
    print("ЦЕЛЬ     : " + describe(dst))
    print("=" * 72)

    # 1. Главный предохранитель: источник и цель — разные чаты.
    if src_key == dst_key:
        die("ИСТОЧНИК И ЦЕЛЬ — ОДИН И ТОТ ЖЕ ЧАТ. Перепост в самого себя запрещён.\n"
            "       SRC_CHAT='" + cfg.src_raw + "' и DST_CHAT='" + cfg.dst_raw + "' "
            "дали один id=" + str(src_key) + ".")

    # Тот же чат может прийти под разными записями (id и @username) — сверяем и их.
    su = getattr(src, "username", None)
    du = getattr(dst, "username", None)
    if su and du and su.lower() == du.lower():
        die("у источника и цели одинаковый username @" + su + " — это один чат.")

    # 2. Цель не должна быть личным чатом или «Избранным»: промах по id пользователя —
    #    самая частая ошибка конфигурации, и она мгновенно видна получателю.
    if isinstance(dst, User):
        die("ЦЕЛЬ — личный чат (" + describe(dst) + "), а нужен канал или группа.")

    # 3. Право писать в цель проверяем ДО первой отправки, а не по факту ошибки.
    if isinstance(dst, Channel) and dst.broadcast:
        rights = getattr(dst, "admin_rights", None)
        if not (getattr(dst, "creator", False) or (rights and rights.post_messages)):
            die("в целевом канале " + describe(dst) + " нет прав на публикацию.\n"
                "       Дайте аккаунту право «Публиковать сообщения» или укажите другой канал.")

    # 4. Независимая сверка по названию: если ждали одно, а открылось другое —
    #    значит, ошиблись id, и продолжать нельзя.
    if cfg.dst_title_expected:
        actual = (getattr(dst, "title", "") or "").strip()
        if actual != cfg.dst_title_expected:
            die("название цели не совпало с DST_TITLE_EXPECTED.\n"
                "       ожидалось: «" + cfg.dst_title_expected + "»\n"
                "       открылось: «" + actual + "»\n"
                "       Либо указан не тот чат, либо канал переименовали — проверьте .env.")
    else:
        log.warning("DST_TITLE_EXPECTED не задан — контрольная сверка названия цели отключена")

    # 5. Источник должен читаться, иначе перепост «успешно» перенесёт ноль сообщений.
    try:
        probe = await client.get_messages(src, limit=1)
    except Exception as e:
        die("не удалось прочитать историю источника: " + str(e))
    if not probe:
        log.warning("в источнике не видно ни одного сообщения — проверьте, тот ли это чат")

    # 6. Привязка: пара чатов фиксируется на первом запуске. Опечатка в .env позже
    #    не сможет молча увести поток в другой канал.
    bound_src, bound_dst = state.get("src_id"), state.get("dst_id")
    if bound_src is None:
        state.set("src_id", str(src_key))
        state.set("dst_id", str(dst_key))
        state.set("dst_title", getattr(dst, "title", "") or "")
        state.set("bound_at", dt.datetime.now(dt.timezone.utc).isoformat())
        print("Привязка «источник → цель» зафиксирована в состоянии.")
    elif (int(bound_src), int(bound_dst)) != (src_key, dst_key):
        if not cfg.allow_retarget:
            die("конфиг указывает на другую пару чатов, чем при первом запуске.\n"
                "       было : src=" + bound_src + " -> dst=" + bound_dst +
                " («" + str(state.get("dst_title")) + "»)\n"
                "       стало: src=" + str(src_key) + " -> dst=" + str(dst_key) + "\n"
                "       Если смена намеренная — ALLOW_RETARGET=true. Иначе исправьте .env.")
        log.warning("ALLOW_RETARGET=true: пара чатов изменена на src=%s -> dst=%s", src_key, dst_key)
        state.set("src_id", str(src_key))
        state.set("dst_id", str(dst_key))
        state.set("dst_title", getattr(dst, "title", "") or "")

    if cfg.forward and getattr(src, "noforwards", False):
        log.warning("в источнике включена защита контента: пересылка невозможна, "
                    "сообщения пойдут копиями")
        print("ДОСТАВКА : копирование (источник запрещает пересылку)")
    elif cfg.forward:
        print("ДОСТАВКА : пересылка — в цели будет пометка «Переслано из …»")
    else:
        print("ДОСТАВКА : копирование (FORWARD=false) — без пометки «переслано»")

    print("Проверки пройдены. Ранее уже перенесено: " + str(state.count(src_key)) + " сообщений.")
    if cfg.dry_run:
        print("РЕЖИМ: DRY_RUN — ничего не отправляется, только лог того, что было бы отправлено.")
    return src, dst


class Sender:
    """Единственная точка отправки. Перед каждым вызовом сверяет получателя."""

    def __init__(self, client: TelegramClient, cfg: Config, state: State, src, dst):
        self.client = client
        self.cfg = cfg
        self.state = state
        self.src = src
        self.dst = dst
        self.src_key = peer_key(src)
        self.dst_key = peer_key(dst)
        self.lock = asyncio.Lock()
        self.sent = 0
        self.skipped = 0
        self.failed = 0
        # Источник с защитой контента: пересылать нельзя, только скачать и загрузить заново.
        self.src_protected = bool(getattr(src, "noforwards", False))
        # Пересылка ссылается на исходное сообщение, поэтому чат с защитой контента
        # её не отдаст — такой источник сразу переводим на копирование.
        self.forwarding = cfg.forward and not self.src_protected

    def verb(self) -> str:
        """Что уже сделано с сообщением — для лога после отправки."""
        return "переслано" if self.forwarding else "скопировано"

    def plan(self) -> str:
        """Что было бы сделано — для холостого прогона, где ещё ничего не ушло."""
        return "переслать" if self.forwarding else "скопировать"

    def guard(self):
        # Последний рубеж: даже если объект цели где-то подменили, дальше не пойдём.
        if self.dst_key == self.src_key:
            die("send_guard: получатель совпал с источником — отправка прервана.")
        if peer_key(self.dst) != self.dst_key:
            die("send_guard: объект цели изменился после проверок — отправка прервана.")

    async def call(self, factory):
        """Отправка с обработкой FloodWait: ждём ровно столько, сколько просит Telegram."""
        for attempt in range(5):
            try:
                return await factory()
            except FloodWaitError as e:
                wait = int(e.seconds) + 2
                log.warning("FloodWait %s c (попытка %s) — ждём", wait, attempt + 1)
                await asyncio.sleep(wait)
        raise RuntimeError("не удалось отправить после 5 попыток из-за FloodWait")

    async def with_author(self, msg, text: str, entities, limit: int):
        """Приписывает строку с автором и возвращает (текст, разметка, отдельная_строка).

        Если подпись не влезает в лимит сообщения (4096) или подписи (1024),
        отдаём её третьим элементом — она уйдёт отдельным сообщением перед контентом.
        """
        if not self.cfg.show_author:
            return text, entities, None
        name = await author_of(msg)
        if not name:
            return text, entities, None

        prefix = self.cfg.author_template.replace("{name}", name)
        delta = utf16len(prefix)
        if delta + utf16len(text) > limit:
            return text, entities, prefix.rstrip("\n")

        # Имя выделяем жирным, перевод строки в выделение не включаем.
        bold = MessageEntityBold(offset=0, length=utf16len(prefix.rstrip("\n")))
        return prefix + text, [bold] + shift_entities(entities, delta), None

    async def forward(self, msgs):
        """Пересылка пачки сообщений одним вызовом.

        Telegram сам ставит «Переслано из …», сохраняет разметку, медиа и склейку
        альбома, ничего не скачивая. Возвращает id сообщений в цели.
        """
        self.guard()
        res = await self.call(lambda: self.client.forward_messages(
            self.dst, [m.id for m in msgs], self.src,
        ))
        if not isinstance(res, list):
            res = [res]
        return [getattr(r, "id", None) for r in res]

    async def deliver(self, group):
        """Единственный вход для доставки 1..N сообщений. Возвращает id в цели.

        Сначала пробуем переслать. Если чат-источник запрещает пересылку, Telegram
        отвечает ChatForwardsRestrictedError — тогда переключаемся на копирование
        до конца запуска, чтобы не биться в запрет на каждом сообщении.
        """
        if self.forwarding:
            try:
                return await self.forward(group)
            except ChatForwardsRestrictedError:
                log.warning("источник запрещает пересылку — дальше копируем "
                            "(поставьте FORWARD=false, чтобы не пробовать её впредь)")
                self.forwarding = False
        if len(group) == 1:
            msg = group[0]
            if has_real_media(msg):
                return [await self.send_single_media(msg)]
            return [await self.send_text(msg)]
        return [getattr(r, "id", None) for r in await self.send_album(group)]

    async def send_text(self, msg):
        self.guard()
        preview = isinstance(msg.media, MessageMediaWebPage)
        text, entities, separate = await self.with_author(
            msg, msg.message or "", msg.entities, 4096)
        if separate:
            await self.call(lambda: self.client.send_message(self.dst, message=separate))
        res = await self.call(lambda: self.client.send_message(
            self.dst,
            message=text,
            formatting_entities=entities or None,
            link_preview=preview,
        ))
        return getattr(res, "id", None)

    async def send_single_media(self, msg):
        self.guard()
        # У подписи к медиа лимит 1024 символа, а не 4096, как у текста.
        caption, entities, separate = await self.with_author(
            msg, msg.message or "", msg.entities, 1024)
        entities = entities or None
        if separate:
            await self.call(lambda: self.client.send_message(self.dst, message=separate))

        if not self.src_protected:
            try:
                res = await self.call(lambda: self.client.send_file(
                    self.dst, msg.media, caption=caption, formatting_entities=entities,
                ))
                return getattr(res, "id", None)
            except Exception as e:
                log.info("прямая отправка медиа не прошла (%s), качаем файл", type(e).__name__)

        with tempfile.TemporaryDirectory() as tmp:
            path = await self.client.download_media(msg, file=tmp)
            if not path:
                raise RuntimeError("не удалось скачать медиа")
            doc = getattr(msg, "document", None)
            attrs = list(doc.attributes) if doc else None
            mime = doc.mime_type if doc else None
            res = await self.call(lambda: self.client.send_file(
                self.dst, path, caption=caption, formatting_entities=entities,
                attributes=attrs, mime_type=mime,
            ))
            return getattr(res, "id", None)

    async def send_album(self, group):
        self.guard()
        captions = [(m.message or "") for m in group]
        # В альбоме подписи уходят без разметки, поэтому автора приписываем
        # обычным текстом к первой подписи.
        if self.cfg.show_author:
            name = await author_of(group[0])
            if name:
                prefix = self.cfg.author_template.replace("{name}", name)
                if utf16len(prefix) + utf16len(captions[0]) <= 1024:
                    captions[0] = prefix + captions[0]
                else:
                    await self.call(lambda: self.client.send_message(
                        self.dst, message=prefix.rstrip("\n")))

        if not self.src_protected:
            try:
                res = await self.call(lambda: self.client.send_file(
                    self.dst, [m.media for m in group], caption=captions,
                ))
                return res if isinstance(res, list) else [res]
            except Exception as e:
                log.info("прямая отправка альбома не прошла (%s), качаем файлы", type(e).__name__)

        with tempfile.TemporaryDirectory() as tmp:
            paths = []
            for m in group:
                p = await self.client.download_media(m, file=tmp)
                if p:
                    paths.append(p)
            if not paths:
                raise RuntimeError("не удалось скачать ни одного файла альбома")
            res = await self.call(lambda: self.client.send_file(
                self.dst, paths, caption=captions[:len(paths)],
            ))
            return res if isinstance(res, list) else [res]


# ------------------------------------------------------------------------ handling


def has_real_media(msg) -> bool:
    return msg.media is not None and not isinstance(msg.media, MessageMediaWebPage)


def skip_reason(msg, cfg: Config):
    """Возвращает (причина, навсегда) или (None, False).

    «Навсегда» = такое сообщение не станет пригодным при другой конфигурации, его
    можно пометить обработанным. Пропуски по фильтрам INCLUDE_* помечать нельзя:
    иначе включённые позже медиа уже не будут перенесены.
    """
    if isinstance(msg, MessageService):
        return "служебное сообщение", True
    if has_real_media(msg):
        if getattr(msg, "document", None) is not None and not cfg.include_documents:
            return "документ (INCLUDE_DOCUMENTS=false)", False
        if getattr(msg, "document", None) is None and not cfg.include_media:
            return "фото/видео (INCLUDE_MEDIA=false)", False
    elif not (msg.message or "").strip():
        return "пустое сообщение", True
    return None, False


def short(msg) -> str:
    text = (msg.message or "").replace("\n", " ")[:60]
    kind = "медиа" if has_real_media(msg) else "текст"
    return "#" + str(msg.id) + " [" + kind + "] " + text


async def handle_one(sender: Sender, cfg: Config, msg, delay_ms: int) -> None:
    if sender.state.already_posted(sender.src_key, msg.id):
        sender.skipped += 1
        log.debug("уже перенесено: %s", short(msg))
        return
    reason, permanent = skip_reason(msg, cfg)
    if reason:
        sender.skipped += 1
        # Помечаем только то, что не станет пригодным при другой конфигурации,
        # и только в боевом режиме: холостой прогон состояние не трогает.
        if permanent and not cfg.dry_run:
            sender.state.mark(sender.src_key, msg.id, sender.dst_key, None)
        log.info("пропуск (%s): %s", reason, short(msg))
        return

    if cfg.dry_run:
        sender.sent += 1
        print("[DRY] " + sender.plan() + ": " + short(msg))
        return

    try:
        dst_ids = await sender.deliver([msg])
        dst_msg_id = dst_ids[0] if dst_ids else None
        sender.state.mark(sender.src_key, msg.id, sender.dst_key, dst_msg_id)
        sender.sent += 1
        log.info("%s %s -> #%s", sender.verb(), short(msg), dst_msg_id)
    except Exception as e:
        sender.failed += 1
        log.error("ОШИБКА на %s: %s: %s", short(msg), type(e).__name__, e)
    await asyncio.sleep(delay_ms / 1000)


async def handle_group(sender: Sender, cfg: Config, group, delay_ms: int) -> None:
    group = [m for m in group if not sender.state.already_posted(sender.src_key, m.id)]
    group = [m for m in group if skip_reason(m, cfg)[0] is None]
    if not group:
        return
    if len(group) == 1:
        await handle_one(sender, cfg, group[0], delay_ms)
        return

    if cfg.dry_run:
        sender.sent += len(group)
        print("[DRY] " + sender.plan() + " альбом из " + str(len(group)) + ": "
              + ", ".join(short(m) for m in group))
        return

    try:
        dst_ids = await sender.deliver(group)
        for i, m in enumerate(group):
            dst_msg_id = dst_ids[i] if i < len(dst_ids) else None
            sender.state.mark(sender.src_key, m.id, sender.dst_key, dst_msg_id)
        sender.sent += len(group)
        log.info("%s альбом из %s (первое %s)", sender.verb(), len(group), short(group[0]))
    except Exception as e:
        sender.failed += len(group)
        log.error("ОШИБКА на альбоме %s: %s: %s", short(group[0]), type(e).__name__, e)
    await asyncio.sleep(delay_ms / 1000)


# ------------------------------------------------------------------------ backfill


async def source_messages(client: TelegramClient, cfg: Config, src):
    """Сообщения источника в хронологическом порядке, от старых к новым.

    HISTORY_LIMIT=N означает «последние N», как этого и ждёшь: сначала берём N
    новейших, потом разворачиваем. Просто limit при обходе от старых дал бы
    N САМЫХ СТАРЫХ сообщений чата — совсем не то.
    """
    if cfg.history_limit:
        kwargs = {"limit": cfg.history_limit}
        if cfg.history_min_id:
            kwargs["min_id"] = cfg.history_min_id
        if cfg.history_since:
            log.warning("HISTORY_SINCE игнорируется: задан HISTORY_LIMIT (последние %s)",
                        cfg.history_limit)
        for msg in reversed(await client.get_messages(src, **kwargs)):
            yield msg
        return

    kwargs = {"reverse": True}  # reverse=True => от старых к новым
    if cfg.history_min_id:
        kwargs["min_id"] = cfg.history_min_id
    if cfg.history_since:
        kwargs["offset_date"] = cfg.history_since
    async for msg in client.iter_messages(src, **kwargs):
        yield msg


async def backfill(client: TelegramClient, cfg: Config, sender: Sender) -> None:
    what = ("последние " + str(cfg.history_limit)) if cfg.history_limit else "вся история"
    print("\n--- Перенос истории (" + what + ", от старых к новым) ---")

    pending = []
    pending_group = None

    async def flush():
        nonlocal pending, pending_group
        if pending:
            await handle_group(sender, cfg, pending, cfg.history_delay_ms)
            pending, pending_group = [], None

    async for msg in source_messages(client, cfg, sender.src):
        gid = getattr(msg, "grouped_id", None)
        if cfg.albums and gid is not None:
            if pending_group is not None and gid != pending_group:
                await flush()
            pending_group = gid
            pending.append(msg)
            continue
        await flush()
        await handle_one(sender, cfg, msg, cfg.history_delay_ms)
    await flush()

    print("--- История: отправлено " + str(sender.sent) +
          ", пропущено " + str(sender.skipped) +
          ", ошибок " + str(sender.failed) + " ---\n")


# ---------------------------------------------------------------------------- live


async def run_live(client: TelegramClient, cfg: Config, sender: Sender) -> None:
    # Без упоминания Ctrl+C: в логах фонового контейнера такая подсказка вводит
    # в заблуждение — там Ctrl+C закрывает только просмотр логов.
    print("--- Онлайн-режим: ждём новые сообщения ---")

    @client.on(events.NewMessage(chats=sender.src))
    async def on_new(event):
        # Подписка уже ограничена источником, но проверяем ещё раз: обработчик не
        # должен реагировать на чужой чат, даже если придёт лишнее событие.
        if event.chat_id != sender.src_key:
            return
        if cfg.albums and getattr(event.message, "grouped_id", None) is not None:
            return  # альбом соберёт отдельный обработчик ниже
        async with sender.lock:
            await handle_one(sender, cfg, event.message, cfg.live_delay_ms)

    if cfg.albums:
        @client.on(events.Album(chats=sender.src))
        async def on_album(event):
            if event.chat_id != sender.src_key:
                return
            async with sender.lock:
                await handle_group(sender, cfg, list(event.messages), cfg.live_delay_ms)

    await client.run_until_disconnected()
    # Штатного завершения у онлайн-режима нет: раз отвалились — выходим с ошибкой,
    # чтобы docker (restart: on-failure) поднял процесс заново.
    log.error("соединение с Telegram разорвано — выходим для перезапуска")
    sys.exit(1)


# ---------------------------------------------------------------------------- main


async def list_dialogs(client: TelegramClient) -> None:
    print("{:>16}  {:<12} {}".format("id", "тип", "название"))
    print("-" * 72)
    async for d in client.iter_dialogs():
        e = d.entity
        uname = " @" + e.username if getattr(e, "username", None) else ""
        print("{:>16}  {:<12} {}".format(peer_key(e), kind_of(e), d.name + uname))


async def amain(args) -> None:
    load_dotenv()
    logging.basicConfig(
        level=(os.getenv("LOG_LEVEL") or "INFO").upper(),
        format="%(asctime)s %(levelname)-7s %(message)s",
        datefmt="%H:%M:%S",
    )
    logging.getLogger("telethon").setLevel(logging.WARNING)

    cfg = Config.load(require_chats=not args.dialogs)
    check_session(cfg.session)
    client = TelegramClient(
        StringSession(cfg.session), cfg.api_id, cfg.api_hash,
        proxy=parse_proxy(cfg.proxy_url),
    )
    # Текст переносим как есть: разметка берётся из entities исходного сообщения,
    # а не парсится заново (иначе символы _ * ` в тексте ломают сообщение).
    client.parse_mode = None

    try:
        await client.connect()
    except Exception as e:
        die("не удалось подключиться к Telegram (" + type(e).__name__ + "): " + str(e) + "\n"
            "       Если сервер не ходит в Telegram напрямую — задайте TG_PROXY_URL\n"
            "       (тот же SOCKS5, что у бэкенда в TELEGRAM_PROXY_URL), например\n"
            "       TG_PROXY_URL=socks5://host:port", code=1)
    if not await client.is_user_authorized():
        die("сессия недействительна: выполните login.py и пропишите свежий TG_SESSION", code=1)

    if args.dialogs:
        await list_dialogs(client)
        await client.disconnect()
        return

    state = State(cfg.state_db)
    src, dst = await preflight(client, cfg, state)

    if args.check:
        print("\nРежим --check: проверки пройдены, ничего не отправлено.")
        await client.disconnect()
        return

    sender = Sender(client, cfg, state, src, dst)

    # «Канарейка»: одно видимое сообщение в цель до массового переноса — глазами
    # убеждаемся, что поток идёт в нужный чат.
    if not cfg.dry_run and cfg.send_canary and not state.get("canary_sent"):
        sender.guard()
        title = getattr(src, "title", "") or utils.get_display_name(src)
        await client.send_message(
            dst,
            "✅ Проверка канала: сюда будет идти перепост из «" + title + "». "
            "Если вы видите это сообщение не в том чате — остановите скрипт.",
        )
        state.set("canary_sent", dt.datetime.now(dt.timezone.utc).isoformat())
        print("Канарейка отправлена в цель — убедитесь, что она пришла в нужный чат.")

    if cfg.mode in ("history", "both"):
        await backfill(client, cfg, sender)
    if cfg.mode in ("live", "both"):
        await run_live(client, cfg, sender)

    await client.disconnect()


def main() -> None:
    p = argparse.ArgumentParser(description="Перепост из закрытого чата в закрытый канал")
    p.add_argument("--check", action="store_true", help="только проверки, без отправки")
    p.add_argument("--dialogs", action="store_true", help="показать id всех чатов аккаунта")
    args = p.parse_args()
    try:
        asyncio.run(amain(args))
    except KeyboardInterrupt:
        print("\nОстановлено пользователем.")


if __name__ == "__main__":
    main()
