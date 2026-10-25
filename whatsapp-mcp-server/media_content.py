"""Turn a downloaded WhatsApp media file into MCP content blocks.

Used by the ``get_media_content`` tool so remote MCP clients (which cannot see
the bridge's filesystem) get the file's contents instead of a container path.

Security: only files that resolve to a regular file *directly inside* the
chat's media directory under the bridge store are ever read. The path comes
from the bridge's /api/download result, but it is re-validated here so a
surprising path can never leak whatsapp.db / messages.db / tokens.
"""

from __future__ import annotations

import base64
import mimetypes
import os
import stat
from urllib.parse import quote

from mcp.types import (
    BlobResourceContents,
    CallToolResult,
    ContentBlock,
    EmbeddedResource,
    ImageContent,
    TextContent,
)

DEFAULT_MAX_BYTES = 5 * 1024 * 1024  # 5 MiB
HARD_MAX_BYTES = 10 * 1024 * 1024  # never return more than this, whatever the caller asks

# Never served even if they somehow land in a chat dir.
_FORBIDDEN_BASENAMES = {"whatsapp.db", "messages.db", "tokens.json", ".bridge-token"}

# Images Claude can view as ImageContent.
_VIEWABLE_IMAGE_MIMES = {"image/jpeg", "image/png", "image/gif", "image/webp"}

_TEXTY_APP_MIMES = {
    "application/json",
    "application/xml",
    "application/javascript",
    "application/x-yaml",
    "application/yaml",
    "application/csv",
    "application/x-sh",
    "application/sql",
    "application/rtf",
}

_MEDIA_TYPE_DEFAULT_MIME = {
    "image": "image/jpeg",
    "video": "video/mp4",
    "audio": "audio/ogg",
    "sticker": "image/webp",
}


def chat_dir_name(chat_jid: str) -> str:
    """Mirror the bridge's directory naming (main.go downloadMedia)."""
    return chat_jid.replace(":", "_")


def resolve_media_path(path: str, store_root: str, chat_jid: str) -> str:
    """Return the real path if it is a regular file directly inside the chat's
    media dir under ``store_root``; raise ValueError otherwise."""
    if not path or not chat_jid or "/" in chat_jid or "\\" in chat_jid or chat_jid in (".", ".."):
        raise ValueError("invalid media path or chat JID")
    root = os.path.realpath(store_root)
    chat_dir = os.path.realpath(os.path.join(root, chat_dir_name(chat_jid)))
    if os.path.dirname(chat_dir) != root:
        raise ValueError("chat directory escapes the media store")
    real = os.path.realpath(path)
    if os.path.dirname(real) != chat_dir:
        raise ValueError("media path is outside the chat's media directory")
    if os.path.basename(real).lower() in _FORBIDDEN_BASENAMES:
        raise ValueError("refusing to read a protected file")
    st = os.stat(real)
    if not stat.S_ISREG(st.st_mode):
        raise ValueError("media path is not a regular file")
    return real


def sniff_mime(head: bytes) -> str | None:
    """Best-effort magic-number detection for extension-less files."""
    if head.startswith(b"%PDF"):
        return "application/pdf"
    if head.startswith(b"\x89PNG\r\n\x1a\n"):
        return "image/png"
    if head.startswith(b"\xff\xd8\xff"):
        return "image/jpeg"
    if head[:6] in (b"GIF87a", b"GIF89a"):
        return "image/gif"
    if head[:4] == b"RIFF" and head[8:12] == b"WEBP":
        return "image/webp"
    if head.startswith(b"OggS"):
        return "audio/ogg"
    if head[4:8] == b"ftyp":
        return "video/mp4"
    if head.startswith(b"PK\x03\x04"):
        return "application/zip"
    return None


def guess_mime(original_name: str | None, saved_path: str, media_type: str | None, head: bytes) -> str:
    for name in (original_name, saved_path):
        if name:
            guessed, _ = mimetypes.guess_type(name)
            if guessed:
                return guessed
    sniffed = sniff_mime(head)
    if sniffed:
        return sniffed
    if media_type in _MEDIA_TYPE_DEFAULT_MIME:
        return _MEDIA_TYPE_DEFAULT_MIME[media_type]
    return "application/octet-stream"


def _is_texty_mime(mime: str) -> bool:
    return mime.startswith("text/") or mime in _TEXTY_APP_MIMES or mime.endswith(("+json", "+xml"))


def _looks_like_text(sample: bytes) -> bool:
    if not sample or b"\x00" in sample:
        return False
    try:
        sample.decode("utf-8")
        return True
    except UnicodeDecodeError as exc:
        # A multibyte char cut at the end of the sample is still text.
        return exc.start >= len(sample) - 3


def build_media_content(
    real_path: str,
    *,
    original_name: str | None,
    media_type: str | None,
    max_bytes: int = DEFAULT_MAX_BYTES,
) -> CallToolResult:
    """Read ``real_path`` (already validated) and build the tool result."""
    max_bytes = max(1, min(int(max_bytes), HARD_MAX_BYTES))
    size = os.path.getsize(real_path)
    with open(real_path, "rb") as fh:
        data = fh.read(max_bytes)
    truncated = size > len(data)

    mime = guess_mime(original_name, real_path, media_type, data[:64])
    display_name = original_name or os.path.basename(real_path)
    meta = f"filename: {display_name}\nmimetype: {mime}\nsize_bytes: {size}\nmedia_type: {media_type or 'unknown'}"

    is_text = (_is_texty_mime(mime) and _looks_like_text(data[:8192])) or (
        mime == "application/octet-stream" and _looks_like_text(data[:8192])
    )
    if is_text:
        text = data.decode("utf-8", errors="replace")
        if mime == "application/octet-stream":
            mime = "text/plain"
            meta = meta.replace("mimetype: application/octet-stream", "mimetype: text/plain (detected)")
        note = (
            f"\ntruncated: true (returned first {len(data)} of {size} bytes; raise max_bytes up to {HARD_MAX_BYTES})"
            if truncated
            else "\ntruncated: false"
        )
        return CallToolResult(
            content=[TextContent(type="text", text=f"{meta}{note}\n---\n{text}")],
        )

    if truncated:
        # Partial binaries are useless (a cut PDF/image won't parse) — metadata only.
        return CallToolResult(
            content=[
                TextContent(
                    type="text",
                    text=(
                        f"{meta}\nerror: file is larger than max_bytes={max_bytes}; content not returned. "
                        f"Retry with a larger max_bytes (hard cap {HARD_MAX_BYTES} bytes) if appropriate."
                    ),
                )
            ],
            isError=True,
        )

    b64 = base64.b64encode(data).decode("ascii")
    blocks: list[ContentBlock] = [TextContent(type="text", text=meta)]
    if mime in _VIEWABLE_IMAGE_MIMES:
        blocks.append(ImageContent(type="image", data=b64, mimeType=mime))
    else:
        blocks.append(
            EmbeddedResource(
                type="resource",
                resource=BlobResourceContents(
                    uri=f"whatsapp-media:///{quote(display_name)}",
                    mimeType=mime,
                    blob=b64,
                ),
            )
        )
    return CallToolResult(content=blocks)
