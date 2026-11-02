import sqlite3
from pathlib import Path

import pytest

from baatplassen import crawl, db


@pytest.fixture
def conn() -> sqlite3.Connection:
    connection = sqlite3.connect(":memory:")
    connection.executescript(db.SCHEMA)
    return connection


@pytest.fixture
def steps(monkeypatch: pytest.MonkeyPatch) -> list[str]:
    calls: list[str] = []
    monkeypatch.setattr(crawl, "discover", lambda *_: calls.append("discover"))
    monkeypatch.setattr(crawl, "crawl", lambda *_: calls.append("crawl"))
    return calls


def test_discovers_first_and_then_crawls(
    conn: sqlite3.Connection, steps: list[str]
) -> None:
    wait = crawl.run_once(conn, 1.0, now=1000.0)
    assert steps == ["discover", "crawl"]
    assert wait == crawl.DISCOVER_INTERVAL_SECONDS


def test_restart_within_a_day_only_crawls(
    conn: sqlite3.Connection, steps: list[str]
) -> None:
    crawl.run_once(conn, 1.0, now=1000.0)
    steps.clear()
    wait = crawl.run_once(conn, 1.0, now=1000.0 + 3600)
    assert steps == ["crawl"]
    assert wait == crawl.DISCOVER_INTERVAL_SECONDS - 3600


def test_discovers_again_after_a_day(
    conn: sqlite3.Connection, steps: list[str]
) -> None:
    crawl.run_once(conn, 1.0, now=1000.0)
    steps.clear()
    crawl.run_once(conn, 1.0, now=1000.0 + crawl.DISCOVER_INTERVAL_SECONDS)
    assert steps == ["discover", "crawl"]


def test_failed_discovery_is_retried_next_time(
    conn: sqlite3.Connection, monkeypatch: pytest.MonkeyPatch
) -> None:
    def fail(*_: object) -> None:
        raise crawl.CrawlAborted("site down")

    monkeypatch.setattr(crawl, "discover", fail)
    with pytest.raises(crawl.CrawlAborted):
        crawl.run_once(conn, 1.0, now=1000.0)
    assert crawl.last_discovery(conn) is None


def test_only_one_crawler_can_hold_the_lock(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr(db, "DB_PATH", tmp_path / "archive.db")
    lock = crawl.acquire_lock()
    with pytest.raises(SystemExit):
        crawl.acquire_lock()
    lock.close()
    crawl.acquire_lock().close()
