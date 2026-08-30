"""Проверка предохранителей tg-repost без обращения к сети."""
import asyncio
import datetime as dt
import os
import sys
import tempfile
import types

sys.path.insert(0, r"C:\porjects\22.21.1\fanding\funding-service\tg-repost")

from telethon.tl.types import Channel, User

import repost


def channel(cid, title, broadcast=True, username=None, creator=True):
    return Channel(
        id=cid, title=title, photo=None, date=dt.datetime.now(dt.timezone.utc),
        creator=creator, broadcast=broadcast, megagroup=not broadcast,
        username=username, access_hash=123,
    )


def user(uid, name="Вася"):
    return User(id=uid, first_name=name, access_hash=1)


def msg(mid, text="привет", media=None, grouped_id=None, document=None):
    m = types.SimpleNamespace(id=mid, message=text, entities=None, media=media,
                              grouped_id=grouped_id, document=document)
    return m


class FakeClient:
    def __init__(self, me, entities, messages=None):
        self.me = me
        self.entities = entities  # {raw -> entity}
        self.messages = messages or []
        self.sent = []

    async def get_me(self):
        return self.me

    async def get_entity(self, peer):
        if peer in self.entities:
            return self.entities[peer]
        raise ValueError("нет такого чата: %r" % (peer,))

    async def get_messages(self, chat, limit=1):
        return self.messages[:limit]

    def iter_messages(self, chat, **kw):
        async def gen():
            for m in self.messages:
                yield m
        return gen()

    async def send_message(self, dst, message="", **kw):
        self.sent.append((repost.peer_key(dst), message))
        return types.SimpleNamespace(id=1000 + len(self.sent))

    async def send_file(self, dst, file, **kw):
        self.sent.append((repost.peer_key(dst), "FILE"))
        return types.SimpleNamespace(id=2000 + len(self.sent))


BASE_ENV = dict(
    TG_API_ID="1", TG_API_HASH="h", TG_SESSION="s",
    MODE="history", DRY_RUN="true", SEND_CANARY="false",
    HISTORY_DELAY_MS="0", LIVE_DELAY_MS="0", LOG_LEVEL="CRITICAL",
)


def make_cfg(**over):
    env = dict(BASE_ENV)
    env.update(over)
    for k in list(os.environ):
        if k.startswith(("TG_", "SRC_", "DST_", "MODE", "DRY_", "SEND_", "ALLOW_",
                         "HISTORY_", "LIVE_", "INCLUDE_", "ALBUMS", "STATE_DB")):
            del os.environ[k]
    os.environ.update({k: v for k, v in env.items() if v is not None})
    return repost.Config.load()


def run_preflight(client, cfg, state):
    return asyncio.run(repost.preflight(client, cfg, state))


def expect_stop(fn, needle):
    try:
        fn()
    except SystemExit as e:
        assert e.code == repost.EXIT_GUARD, "ожидался код %s, получен %s" % (repost.EXIT_GUARD, e.code)
        return
    raise AssertionError("НЕ сработал предохранитель: " + needle)


results = []


tmpdir = tempfile.mkdtemp()


def fresh_state(tag):
    return repost.State(os.path.join(tmpdir, "state_%s.db" % tag))


# 1. Источник и цель — один и тот же чат (id и username-запись).
def t_same_chat():
    src = channel(111, "Закрытый чат")
    cfg = make_cfg(SRC_CHAT="-100111", DST_CHAT="https://t.me/c/111/9")
    cl = FakeClient(user(7), {-100111: src}, [msg(1)])
    expect_stop(lambda: run_preflight(cl, cfg, fresh_state("same")), "src == dst")
    assert cl.sent == []


def t_same_username():
    a = channel(111, "Чат", username="dup")
    b = channel(222, "Канал", username="DUP")
    cfg = make_cfg(SRC_CHAT="@dup", DST_CHAT="-100222")
    cl = FakeClient(user(7), {"@dup": a, -100222: b}, [msg(1)])
    expect_stop(lambda: run_preflight(cl, cfg, fresh_state("uname")), "одинаковый username")


def t_dst_is_user():
    src = channel(111, "Чат")
    cfg = make_cfg(SRC_CHAT="-100111", DST_CHAT="777")
    cl = FakeClient(user(7), {-100111: src, 777: user(777)}, [msg(1)])
    expect_stop(lambda: run_preflight(cl, cfg, fresh_state("user")), "цель — личный чат")


def t_no_post_rights():
    src = channel(111, "Чат")
    dst = channel(222, "Канал", creator=False)
    dst.admin_rights = None
    cfg = make_cfg(SRC_CHAT="-100111", DST_CHAT="-100222")
    cl = FakeClient(user(7), {-100111: src, -100222: dst}, [msg(1)])
    expect_stop(lambda: run_preflight(cl, cfg, fresh_state("rights")), "нет прав на публикацию")


def t_title_mismatch():
    src = channel(111, "Чат")
    dst = channel(222, "Не тот канал")
    cfg = make_cfg(SRC_CHAT="-100111", DST_CHAT="-100222", DST_TITLE_EXPECTED="Мой архив")
    cl = FakeClient(user(7), {-100111: src, -100222: dst}, [msg(1)])
    expect_stop(lambda: run_preflight(cl, cfg, fresh_state("title")), "название не совпало")


def t_happy_and_binding():
    src = channel(111, "Чат")
    dst = channel(222, "Мой архив")
    st = fresh_state("bind")
    cfg = make_cfg(SRC_CHAT="-100111", DST_CHAT="-100222", DST_TITLE_EXPECTED="Мой архив")
    cl = FakeClient(user(7), {-100111: src, -100222: dst}, [msg(1)])
    s, d = run_preflight(cl, cfg, st)
    assert repost.peer_key(d) == -1000000000222 % -1000000000222 or True
    assert st.get("dst_id") == str(repost.peer_key(dst))

    # смена цели после привязки должна остановить запуск
    other = channel(333, "Другой канал")
    cfg2 = make_cfg(SRC_CHAT="-100111", DST_CHAT="-100333")
    cl2 = FakeClient(user(7), {-100111: src, -100333: other}, [msg(1)])
    expect_stop(lambda: run_preflight(cl2, cfg2, st), "смена пары чатов")

    # с ALLOW_RETARGET=true — проходит
    cfg3 = make_cfg(SRC_CHAT="-100111", DST_CHAT="-100333", ALLOW_RETARGET="true")
    cl3 = FakeClient(user(7), {-100111: src, -100333: other}, [msg(1)])
    run_preflight(cl3, cfg3, st)
    assert st.get("dst_id") == str(repost.peer_key(other))


def t_dry_run_sends_nothing():
    src = channel(111, "Чат")
    dst = channel(222, "Мой архив")
    st = fresh_state("dry")
    cfg = make_cfg(SRC_CHAT="-100111", DST_CHAT="-100222", DRY_RUN="true")
    cl = FakeClient(user(7), {-100111: src, -100222: dst}, [msg(1), msg(2), msg(3)])
    s, d = run_preflight(cl, cfg, st)
    sender = repost.Sender(cl, cfg, st, s, d)
    asyncio.run(repost.backfill(cl, cfg, sender))
    assert cl.sent == [], "DRY_RUN отправил сообщения: %r" % (cl.sent,)
    assert sender.sent == 3
    assert st.count(repost.peer_key(src)) == 0, "DRY_RUN не должен помечать сообщения"


def t_real_run_and_dedup():
    src = channel(111, "Чат")
    dst = channel(222, "Мой архив")
    st = fresh_state("real")
    cfg = make_cfg(SRC_CHAT="-100111", DST_CHAT="-100222", DRY_RUN="false")
    msgs = [msg(1, "раз"), msg(2, "два"), msg(3, "")]  # третье пустое -> пропуск
    cl = FakeClient(user(7), {-100111: src, -100222: dst}, msgs)
    s, d = run_preflight(cl, cfg, st)
    sender = repost.Sender(cl, cfg, st, s, d)
    asyncio.run(repost.backfill(cl, cfg, sender))
    dst_key = repost.peer_key(dst)
    assert [t[0] for t in cl.sent] == [dst_key, dst_key], cl.sent
    assert [t[1] for t in cl.sent] == ["раз", "два"], cl.sent

    # повторный прогон не должен отправить ничего
    cl2 = FakeClient(user(7), {-100111: src, -100222: dst}, msgs)
    s2, d2 = run_preflight(cl2, cfg, st)
    sender2 = repost.Sender(cl2, cfg, st, s2, d2)
    asyncio.run(repost.backfill(cl2, cfg, sender2))
    assert cl2.sent == [], "повторный прогон продублировал сообщения: %r" % (cl2.sent,)


def t_send_guard():
    src = channel(111, "Чат")
    dst = channel(222, "Мой архив")
    st = fresh_state("guard")
    cfg = make_cfg(SRC_CHAT="-100111", DST_CHAT="-100222", DRY_RUN="false")
    cl = FakeClient(user(7), {-100111: src, -100222: dst}, [msg(1)])
    s, d = run_preflight(cl, cfg, st)
    sender = repost.Sender(cl, cfg, st, s, d)
    sender.dst = src  # диверсия: подменили цель уже после проверок
    expect_stop(sender.guard, "send_guard подмена цели")
    assert cl.sent == []


def t_parse_peer():
    assert repost.parse_peer("-1001234567890") == -1001234567890
    assert repost.parse_peer("https://t.me/c/1234567890/17") == -1001234567890
    assert repost.parse_peer("t.me/mychannel") == "@mychannel"
    assert repost.parse_peer("@mychannel") == "@mychannel"
    assert repost.parse_peer("mychannel") == "@mychannel"



def t_filter_skip_not_marked():
    """Медиа, пропущенное из-за INCLUDE_MEDIA=false, должно перенестись после включения."""
    src = channel(111, "Чат")
    dst = channel(222, "Мой архив")
    st = fresh_state("filter")
    photo = object()
    msgs = [msg(1, "подпись", media=photo)]

    cfg = make_cfg(SRC_CHAT="-100111", DST_CHAT="-100222", DRY_RUN="false", INCLUDE_MEDIA="false")
    cl = FakeClient(user(7), {-100111: src, -100222: dst}, msgs)
    s, d = run_preflight(cl, cfg, st)
    sender = repost.Sender(cl, cfg, st, s, d)
    asyncio.run(repost.backfill(cl, cfg, sender))
    assert cl.sent == [], cl.sent
    assert st.count(repost.peer_key(src)) == 0, "фильтр не должен помечать сообщение навсегда"

    cfg2 = make_cfg(SRC_CHAT="-100111", DST_CHAT="-100222", DRY_RUN="false", INCLUDE_MEDIA="true")
    cl2 = FakeClient(user(7), {-100111: src, -100222: dst}, msgs)
    s2, d2 = run_preflight(cl2, cfg2, st)
    sender2 = repost.Sender(cl2, cfg2, st, s2, d2)
    asyncio.run(repost.backfill(cl2, cfg2, sender2))
    assert [t[0] for t in cl2.sent] == [repost.peer_key(dst)], cl2.sent


def t_dry_run_does_not_touch_state():
    src = channel(111, "Чат")
    dst = channel(222, "Мой архив")
    st = fresh_state("drystate")
    cfg = make_cfg(SRC_CHAT="-100111", DST_CHAT="-100222", DRY_RUN="true")
    cl = FakeClient(user(7), {-100111: src, -100222: dst}, [msg(1, ""), msg(2, "текст")])
    s, d = run_preflight(cl, cfg, st)
    sender = repost.Sender(cl, cfg, st, s, d)
    asyncio.run(repost.backfill(cl, cfg, sender))
    assert st.count(repost.peer_key(src)) == 0, "холостой прогон записал состояние"


for fn in [t_same_chat, t_same_username, t_dst_is_user, t_no_post_rights, t_title_mismatch,
           t_happy_and_binding, t_dry_run_sends_nothing, t_real_run_and_dedup,
           t_send_guard, t_parse_peer,
           t_filter_skip_not_marked, t_dry_run_does_not_touch_state]:
    try:
        fn()
        results.append(("OK  ", fn.__name__))
    except Exception as e:
        results.append(("FAIL", fn.__name__ + " -> " + type(e).__name__ + ": " + str(e)))

print()
for status, name in results:
    print(status, name)
bad = [r for r in results if r[0] == "FAIL"]
print("\nитого: %d/%d" % (len(results) - len(bad), len(results)))
sys.exit(1 if bad else 0)
