import io
import sqlite3
from pathlib import Path

import httpx
import pytest
from PIL import Image

from baatplassen import db, images, search

UPLOAD = "https://baatplassen.no/i/uploads/monthly_2026_10/a.jpg"


def png_bytes(size: tuple[int, int]) -> bytes:
    output = io.BytesIO()
    Image.new("RGB", size, "red").save(output, "PNG")
    return output.getvalue()


def client_returning(status: int, content: bytes, calls: list[str]) -> httpx.Client:
    def handler(request: httpx.Request) -> httpx.Response:
        calls.append(str(request.url))
        return httpx.Response(status, content=content)

    return httpx.Client(transport=httpx.MockTransport(handler))


@pytest.fixture
def cache(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> sqlite3.Connection:
    monkeypatch.setattr(images, "IMAGES_DB_PATH", tmp_path / "images.db")
    return images.connect_cache()


@pytest.mark.parametrize(
    "url",
    [
        "https://baatplassen.no/bildearkiv/43/a.jpg",
        "https://baatplassen.no/i/theme_resources/a.png",
        "https://example.com/i/uploads/a.jpg",
        "http://192.168.1.1/i/uploads/a.jpg",
        "https://baatplassen.no.evil.example/i/uploads/a.jpg",
    ],
)
def test_only_the_forums_uploads_are_fetched(
    cache: sqlite3.Connection, url: str
) -> None:
    calls: list[str] = []
    client = client_returning(200, png_bytes((10, 10)), calls)
    with pytest.raises(images.ImageUnavailable):
        images.get_jpeg(cache, client, url)
    assert calls == []


def test_image_is_resized_and_cached(cache: sqlite3.Connection) -> None:
    calls: list[str] = []
    client = client_returning(200, png_bytes((3000, 1500)), calls)
    first = images.get_jpeg(cache, client, UPLOAD)
    second = images.get_jpeg(cache, client, UPLOAD)
    assert first == second
    assert len(calls) == 1
    with Image.open(io.BytesIO(first)) as image:
        assert image.format == "JPEG"
        assert image.size == (1568, 784)


@pytest.mark.parametrize(
    ("status", "content"), [(404, b""), (302, b""), (200, b"<html>not an image")]
)
def test_failures_are_reported_and_not_cached(
    cache: sqlite3.Connection, status: int, content: bytes
) -> None:
    client = client_returning(status, content, [])
    with pytest.raises(images.ImageUnavailable):
        images.get_jpeg(cache, client, UPLOAD)
    assert cache.execute("SELECT COUNT(*) FROM images").fetchone()[0] == 0


def test_image_urls() -> None:
    text = f"Se her [bilde: {UPLOAD}] og [bilde: ]"
    assert images.image_urls(text) == [UPLOAD]


def test_find_words_on_a_read_only_database(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    path = tmp_path / "archive.db"
    monkeypatch.setattr(db, "DB_PATH", path)
    conn = db.connect()
    conn.executemany(
        "INSERT INTO posts_fts (title, body) VALUES ('Motor', ?)",
        [("Dieselmotor og påhengsmotor",), ("Ny påhengsmotor",), ("Seil",)],
    )
    conn.commit()

    readonly = db.connect_readonly()
    assert search.find_words(readonly, "*motor")[:2] == [
        {"word": "motor", "posts": 3},
        {"word": "påhengsmotor", "posts": 2},
    ]
    assert search.find_words(readonly, "PÅHENGS") == [
        {"word": "påhengsmotor", "posts": 2}
    ]
    assert search.find_words(readonly, "s?il") == [{"word": "seil", "posts": 1}]
