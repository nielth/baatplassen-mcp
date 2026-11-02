"""Parse Invision Community topic pages and sitemaps from baatplassen.no."""

import json
import re
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any
from xml.etree import ElementTree

from bs4 import BeautifulSoup
from bs4.element import NavigableString, PageElement, Tag

SITEMAP_NS = "{http://www.sitemaps.org/schemas/sitemap/0.9}"
BLOCK_TAGS = {
    "p",
    "div",
    "ul",
    "ol",
    "li",
    "table",
    "tr",
    "h1",
    "h2",
    "h3",
    "h4",
    "h5",
    "h6",
    "pre",
}
SKIP_TAGS = {"script", "style", "noscript"}


class ParseError(Exception):
    """The page did not look like a topic page."""


@dataclass
class Post:
    id: int
    author: str
    author_id: int | None
    posted_at: str | None
    body: str  # The poster's own words: quotes removed. This is what gets indexed.
    full_text: str | None  # Body with quotes inline. None when the post has no quotes.


@dataclass
class TopicPage:
    title: str
    forum_id: int | None
    forum: str | None
    num_pages: int
    expected_posts: int | None
    posts: list[Post]


def parse_sitemap(xml: bytes) -> list[tuple[str, str | None]]:
    """Return (loc, lastmod) for every entry in a sitemap or sitemap index."""
    entries: list[tuple[str, str | None]] = []
    for element in ElementTree.fromstring(xml):
        loc = element.findtext(f"{SITEMAP_NS}loc")
        if loc:
            entries.append((loc.strip(), element.findtext(f"{SITEMAP_NS}lastmod")))
    return entries


def _render(node: PageElement, keep_quotes: bool) -> str:
    """Convert a post's HTML to plain text, keeping links and image URLs."""
    if isinstance(node, NavigableString):
        return re.sub(r"\s+", " ", str(node).replace("\xa0", " "))
    if not isinstance(node, Tag) or node.name in SKIP_TAGS:
        return ""
    if node.has_attr("data-excludequote"):  # "Endret ... av ..." edit notice
        return ""
    if node.name == "br":
        return "\n"
    if node.name == "img":
        is_emoticon = "/emoticons/" in str(node.get("src") or node.get("data-src"))
        if is_emoticon or "ipsEmoji" in node.get_attribute_list("class"):
            return str(node.get("alt") or "")
        return f"[bilde: {node.get('src') or node.get('data-src') or ''}]"
    if node.name == "blockquote":
        if not keep_quotes:
            return ""
        citation = node.select_one(".ipsQuote_citation")
        if citation:
            citation.extract()
        inner = "".join(_render(child, True) for child in node.children)
        lines = [line for line in _clean(inner).split("\n") if line.strip()]
        header = _clean(citation.get_text()) if citation else "Sitat:"
        return "\n" + "\n".join(f"> {line}" for line in [header, *lines]) + "\n\n"

    inner = "".join(_render(child, keep_quotes) for child in node.children)
    if node.name == "a":
        href = str(node.get("href") or "")
        text = inner.strip()
        if href.startswith("http") and text and "[bilde:" not in text and text != href:
            return f"{inner} ({href})"
    if node.name in BLOCK_TAGS:
        return f"\n{inner}\n"
    return inner


def _clean(text: str) -> str:
    lines = [line.strip() for line in text.split("\n")]
    return re.sub(r"\n{3,}", "\n\n", "\n".join(lines)).strip()


def html_to_text(content: Tag, keep_quotes: bool) -> str:
    return _clean("".join(_render(child, keep_quotes) for child in content.children))


def _topic_metadata(soup: BeautifulSoup) -> tuple[str | None, int | None]:
    """Return the title and reply count from the page's JSON-LD."""
    for script in soup.select('script[type="application/ld+json"]'):
        try:
            data = json.loads(script.get_text(), strict=False)
        except json.JSONDecodeError:
            continue
        if not isinstance(data, dict) or data.get("@type") != "DiscussionForumPosting":
            continue
        replies: int | None = None
        for stat in data.get("interactionStatistic", []):
            if str(stat.get("interactionType", "")).endswith("CommentAction"):
                replies = int(stat["userInteractionCount"])
        return data.get("headline"), replies
    return None, None


def _parse_post(article: Tag) -> Post | None:
    match = re.fullmatch(r"elComment_(\d+)", str(article.get("id") or ""))
    content = article.select_one('[data-role="commentContent"]')
    if not match or content is None:
        return None

    quote_data: dict[str, Any] = {}
    wrapper = article.select_one("[data-quotedata]")
    if wrapper is not None:
        try:
            quote_data = json.loads(str(wrapper.get("data-quotedata")))
        except json.JSONDecodeError:
            pass

    author = quote_data.get("username")
    if not author:
        heading = article.select_one("aside h3") or article.select_one("h3")
        author = heading.get_text(strip=True) if heading else "Ukjent"

    posted_at: str | None = None
    if quote_data.get("timestamp"):
        timestamp = int(quote_data["timestamp"])
        posted_at = datetime.fromtimestamp(timestamp, UTC).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
    else:
        time_tag = article.select_one(".ipsComment_meta time[datetime]")
        if time_tag is not None:
            posted_at = str(time_tag.get("datetime"))

    has_quotes = content.find("blockquote") is not None
    return Post(
        id=int(match.group(1)),
        author=author,
        author_id=quote_data.get("userid") or None,
        posted_at=posted_at,
        body=html_to_text(content, keep_quotes=False),
        full_text=html_to_text(content, keep_quotes=True) if has_quotes else None,
    )


def parse_topic_page(html: str) -> TopicPage:
    soup = BeautifulSoup(html, "lxml")
    title, replies = _topic_metadata(soup)
    articles = soup.select("article[id^=elComment_]")
    posts = [post for post in map(_parse_post, articles) if post is not None]
    if title is None or not posts or len(posts) != len(articles):
        raise ParseError(
            f"title={title!r}, articles={len(articles)}, parsed={len(posts)}"
        )

    forum_id: int | None = None
    forum: str | None = None
    breadcrumb = soup.select_one('[data-role="breadcrumbList"]')
    if breadcrumb is not None:
        links = breadcrumb.select('a[href*="/forum/"]')
        if links:
            forum = links[-1].get_text(strip=True)
            found = re.search(r"/forum/(\d+)-", str(links[-1].get("href")))
            forum_id = int(found.group(1)) if found else None

    pagination = soup.select_one("ul.ipsPagination[data-pages]")
    num_pages = int(str(pagination.get("data-pages"))) if pagination else 1
    return TopicPage(
        title=title,
        forum_id=forum_id,
        forum=forum,
        num_pages=num_pages,
        expected_posts=replies + 1 if replies is not None else None,
        posts=posts,
    )
