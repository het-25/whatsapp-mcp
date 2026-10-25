import base64
import os

import pytest

import main
from media_content import HARD_MAX_BYTES, build_media_content, resolve_media_path

JID = "919453966207@s.whatsapp.net"
PNG = b"\x89PNG\r\n\x1a\n" + b"\x00" * 32


@pytest.fixture
def store(tmp_path):
    (tmp_path / JID).mkdir()
    (tmp_path / "messages.db").write_bytes(b"SQLite format 3\x00")
    return tmp_path


def _write(store, name, data, jid=JID):
    p = store / jid / name
    p.write_bytes(data)
    return str(p)


def test_resolve_accepts_file_in_chat_dir(store):
    p = _write(store, "document_20261022_121809_ABC", b"hi")
    assert resolve_media_path(p, str(store), JID) == os.path.realpath(p)


@pytest.mark.parametrize(
    "rel",
    ["messages.db", f"{JID}/../messages.db", "other@s.whatsapp.net/x"],
)
def test_resolve_rejects_outside_chat_dir(store, rel):
    (store / "other@s.whatsapp.net").mkdir()
    (store / "other@s.whatsapp.net" / "x").write_bytes(b"x")
    with pytest.raises(ValueError):
        resolve_media_path(str(store / rel), str(store), JID)


def test_resolve_rejects_symlink_escape_and_bad_jid(store):
    link = store / JID / "evil"
    link.symlink_to(store / "messages.db")
    with pytest.raises(ValueError):
        resolve_media_path(str(link), str(store), JID)
    with pytest.raises(ValueError):
        resolve_media_path(str(store / "messages.db"), str(store), "..")


def test_extensionless_text_document_inline(store):
    p = _write(store, "document_20261022_121809_ABC", "hello\nनमस्ते\n".encode())
    res = build_media_content(p, original_name="notes.txt", media_type="document")
    assert not res.isError
    text = res.content[0].text
    assert "mimetype: text/plain" in text and "truncated: false" in text
    assert text.endswith("hello\nनमस्ते\n")


def test_unknown_name_utf8_detected_as_text(store):
    p = _write(store, "document_x", b"just some text")
    res = build_media_content(p, original_name=None, media_type="document")
    assert "text/plain (detected)" in res.content[0].text


def test_text_truncation(store):
    p = _write(store, "document_big", b"a" * 100)
    res = build_media_content(p, original_name="big.txt", media_type="document", max_bytes=10)
    text = res.content[0].text
    assert "truncated: true" in text and text.endswith("\n---\n" + "a" * 10)


def test_image_returns_image_content(store):
    p = _write(store, "image_1.jpg", PNG)  # wrong extension wins via name; still viewable
    res = build_media_content(p, original_name=None, media_type="image")
    assert res.content[1].type == "image"
    assert base64.b64decode(res.content[1].data) == PNG


def test_pdf_returns_embedded_resource(store):
    data = b"%PDF-1.4\n\x00\x01binary"
    p = _write(store, "document_pdf", data)
    res = build_media_content(p, original_name="Invoice Oct.pdf", media_type="document")
    blob = res.content[1].resource
    assert blob.mimeType == "application/pdf"
    assert base64.b64decode(blob.blob) == data
    assert "Invoice%20Oct.pdf" in str(blob.uri)


def test_extensionless_pdf_sniffed(store):
    p = _write(store, "document_pdf2", b"%PDF-1.7\x00")
    res = build_media_content(p, original_name=None, media_type="document")
    assert res.content[1].resource.mimeType == "application/pdf"


def test_oversize_binary_is_error_without_content(store):
    p = _write(store, "video_1.mp4", b"\x00\x00\x00\x18ftypmp42" + b"\x00" * 100)
    res = build_media_content(p, original_name=None, media_type="video", max_bytes=10)
    assert res.isError and len(res.content) == 1 and "larger than max_bytes" in res.content[0].text


def test_max_bytes_clamped(store):
    p = _write(store, "document_y.txt", b"x")
    res = build_media_content(p, original_name=None, media_type="document", max_bytes=HARD_MAX_BYTES * 10)
    assert not res.isError


def test_tool_end_to_end_with_fake_bridge(store, monkeypatch):
    p = _write(store, "document_20261022_121809_3EB0", b"plain text body")
    monkeypatch.setattr(main, "whatsapp_download_media_with_error", lambda m, c: (p, ""))
    monkeypatch.setattr(main, "whatsapp_media_store_root", lambda: str(store))
    monkeypatch.setattr(main, "whatsapp_get_media_message_info", lambda m, c: ("document", "a.txt"))
    res = main.get_media_content("3EB0", JID)
    assert not res.isError and res.content[0].text.endswith("plain text body")


def test_tool_refuses_path_outside_store(store, monkeypatch):
    monkeypatch.setattr(main, "whatsapp_download_media_with_error", lambda m, c: (str(store / "messages.db"), ""))
    monkeypatch.setattr(main, "whatsapp_media_store_root", lambda: str(store))
    res = main.get_media_content("x", JID)
    assert res.isError and "Refusing" in res.content[0].text


def test_tool_reports_bridge_error(monkeypatch):
    monkeypatch.setattr(main, "whatsapp_download_media_with_error", lambda m, c: (None, "not a media message"))
    res = main.get_media_content("x", JID)
    assert res.isError and "not a media message" in res.content[0].text
