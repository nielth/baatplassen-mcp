"""Download post images on demand and cache them, resized for Claude, in SQLite."""

import io
import re
import sqlite3
from datetime import UTC, datetime
from urllib.parse import urlparse

import httpx
from PIL import Image, ImageOps, UnidentifiedImageError

from baatplassen.crawl import USER_AGENT
from baatplassen.db import DB_PATH

IMAGES_DB_PATH = DB_PATH.with_name("images.db")
SITE_HOSTS = {"baatplassen.no", "www.baatplassen.no"}
MAX_DOWNLOAD_BYTES = 25_000_000
MAX_SIDE_PIXELS = 1568  # Larger images are scaled down by Claude anyway.
JPEG_QUALITY = 85


class ImageUnavailable(Exception):
    """The image can't be shown. The message says why."""


def image_urls(text: str) -> list[str]:
    return re.findall(r"\[bilde: ([^\]\s]+)\]", text)


def refusal_reason(url: str) -> str | None:
    """Say why a URL must not be fetched, or return None if it may be.

    The URLs come from posts written by strangers, so only the forum's own uploads are
    fetched. That also keeps them from pointing this machine at its local network.
    """
    parsed = urlparse(url)
    if parsed.hostname not in SITE_HOSTS:
        return "hosted on another site, so it is not fetched automatically"
    # robots.txt disallows /bildearkiv, but the site's owner has given permission.
    if not parsed.path.startswith(("/i/uploads/", "/bildearkiv/")):
        return "not in the forum's image folders"
    return None


def to_jpeg(data: bytes) -> bytes:
    """Convert to a JPEG small enough to send to Claude."""
    try:
        with Image.open(
            io.BytesIO(data), formats=["JPEG", "PNG", "GIF", "WEBP", "BMP"]
        ) as image:
            converted = ImageOps.exif_transpose(image).convert("RGB")
            converted.thumbnail((MAX_SIDE_PIXELS, MAX_SIDE_PIXELS))
            output = io.BytesIO()
            converted.save(output, "JPEG", quality=JPEG_QUALITY)
    except (UnidentifiedImageError, OSError, ValueError) as error:
        raise ImageUnavailable(f"could not be read as an image: {error}") from error
    except Image.DecompressionBombError as error:
        raise ImageUnavailable("is unreasonably large") from error
    return output.getvalue()


def connect_cache() -> sqlite3.Connection:
    """Open the image cache, kept apart from the archive so the crawl can't block it."""
    IMAGES_DB_PATH.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(IMAGES_DB_PATH, timeout=30)
    conn.execute("""
        CREATE TABLE IF NOT EXISTS images (
            url TEXT PRIMARY KEY,
            jpeg BLOB NOT NULL,
            fetched_at TEXT NOT NULL
        )
        """)
    return conn


def download(client: httpx.Client, url: str) -> bytes:
    """Fetch an image. Redirects are not followed, so it can't leave the forum's site."""
    https_url = urlparse(url)._replace(scheme="https").geturl()
    try:
        with client.stream("GET", https_url) as response:
            if response.status_code != 200:
                raise ImageUnavailable(f"HTTP {response.status_code}")
            data = bytearray()
            for chunk in response.iter_bytes():
                data += chunk
                if len(data) > MAX_DOWNLOAD_BYTES:
                    raise ImageUnavailable("is over the download size limit")
    except httpx.TransportError as error:
        raise ImageUnavailable(f"download failed: {error!r}") from error
    return bytes(data)


def get_jpeg(cache: sqlite3.Connection, client: httpx.Client, url: str) -> bytes:
    """Return the image from the cache, downloading it the first time."""
    reason = refusal_reason(url)
    if reason:
        raise ImageUnavailable(reason)
    row = cache.execute("SELECT jpeg FROM images WHERE url = ?", (url,)).fetchone()
    if row:
        cached: bytes = row[0]
        return cached
    jpeg = to_jpeg(download(client, url))
    cache.execute(
        "INSERT OR REPLACE INTO images (url, jpeg, fetched_at) VALUES (?, ?, ?)",
        (url, jpeg, datetime.now(UTC).isoformat(timespec="seconds")),
    )
    cache.commit()
    return jpeg


def new_client() -> httpx.Client:
    return httpx.Client(
        headers={"User-Agent": USER_AGENT}, follow_redirects=False, timeout=60
    )
