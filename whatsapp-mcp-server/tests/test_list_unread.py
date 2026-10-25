"""Tests for list_unread: the read-only unread digest over messages.db."""

import sqlite3

import pytest

import whatsapp

SCHEMA = """
    CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP, last_read_time TIMESTAMP);
    CREATE TABLE messages (
        id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP, is_from_me BOOLEAN,
        media_type TEXT, filename TEXT, PRIMARY KEY (id, chat_jid)
    );
"""

# (jid, name, last_read_time, [(id, ts, is_from_me, content, media_type)])
CHATS = [
    (
        "alice@s.whatsapp.net",
        "Alice",
        "2024-01-15 09:00:00+00:00",
        [
            ("a1", "2024-01-15 08:00:00+00:00", 0, "already read", None),
            ("a2", "2024-01-15 09:30:00+00:00", 0, "lunch?", None),
            ("a3", "2024-01-15 09:31:00+00:00", 0, "👍", "reaction"),
            ("a4", "2024-01-15 10:00:00+00:00", 0, "ping", None),
        ],
    ),
    (
        "bob@s.whatsapp.net",
        "Bob",
        "2024-01-15 11:00:00+00:00",
        [("b1", "2024-01-15 10:00:00+00:00", 0, "read on phone", None)],
    ),
    (
        "me@s.whatsapp.net",
        "Replied",
        None,
        [
            ("m1", "2024-01-15 09:00:00+00:00", 0, "hi", None),
            ("m2", "2024-01-15 09:05:00+00:00", 1, "hey", None),
        ],
    ),
    (
        "123@g.us",
        "Group",
        None,
        [
            ("g1", "2024-01-10 10:00:00+00:00", 0, "old", None),
            ("g2", "2024-01-15 07:00:00+00:00", 0, "new", None),
        ],
    ),
    (
        "status@broadcast",
        "Status",
        None,
        [("s1", "2024-01-15 12:00:00+00:00", 0, "story", None)],
    ),
]


@pytest.fixture
def db(tmp_path, monkeypatch):
    path = tmp_path / "messages.db"
    conn = sqlite3.connect(path)
    conn.executescript(SCHEMA)
    for jid, name, last_read, msgs in CHATS:
        conn.execute(
            "INSERT INTO chats VALUES (?, ?, ?, ?)",
            (jid, name, max(m[1] for m in msgs), last_read),
        )
        for mid, ts, mine, content, media_type in msgs:
            conn.execute(
                "INSERT INTO messages VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                (mid, jid, "me" if mine else jid, content, ts, mine, media_type, None),
            )
    conn.commit()
    conn.close()
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", str(path))
    monkeypatch.setattr(whatsapp, "get_sender_name", lambda jid: jid)


def test_only_messages_after_read_marker(db):
    chats = {c["jid"]: c for c in whatsapp.list_unread()}
    alice = chats["alice@s.whatsapp.net"]
    assert [m["id"] for m in alice["messages"]] == ["a2", "a4"]  # oldest first, reaction skipped
    assert alice["unread_count"] == 2
    assert alice["truncated"] is False


def test_skips_read_replied_and_status(db):
    jids = {c["jid"] for c in whatsapp.list_unread()}
    assert "bob@s.whatsapp.net" not in jids  # read on another device
    assert "me@s.whatsapp.net" not in jids  # we replied last
    assert "status@broadcast" not in jids


def test_since_bounds_markerless_chats(db):
    group = next(c for c in whatsapp.list_unread() if c["jid"] == "123@g.us")
    assert [m["id"] for m in group["messages"]] == ["g1", "g2"]

    group = next(c for c in whatsapp.list_unread(since="2024-01-14T00:00:00+00:00") if c["jid"] == "123@g.us")
    assert [m["id"] for m in group["messages"]] == ["g2"]

    assert not any(c["jid"] == "alice@s.whatsapp.net" for c in whatsapp.list_unread(since="2024-01-16T00:00:00"))


def test_exclude_groups_and_cap(db):
    assert all(not c["is_group"] for c in whatsapp.list_unread(include_groups=False))
    alice = next(c for c in whatsapp.list_unread(max_messages_per_chat=1) if c["jid"] == "alice@s.whatsapp.net")
    assert [m["id"] for m in alice["messages"]] == ["a4"]  # newest kept when capped
    assert alice["truncated"] is True
