import pytest

from baatplassen.parse import ParseError, parse_sitemap, parse_topic_page

PAGE = """
<html><head>
<script type='application/ld+json'>
{"@type": "DiscussionForumPosting", "headline": "Bytte impeller",
 "text": "line one
line two",
 "interactionStatistic": [{"interactionType": "http://schema.org/CommentAction",
                           "userInteractionCount": 26}]}
</script></head><body>
<ul data-role="breadcrumbList">
  <li><a href="https://baatplassen.no/i/forum/25-forum/">Forum</a></li>
  <li><a href="https://baatplassen.no/i/forum/1-b%C3%A5tforumet/">Båtforumet</a></li>
</ul>
<ul class='ipsPagination' data-pages='2'></ul>
<article id="elComment_10">
  <aside><h3>Fallback</h3></aside>
  <div data-quoteData='{"userid":7,"username":"Ola","timestamp":1040064844}'>
    <div data-role='commentContent'>
      <blockquote class="ipsQuote">
        <div class="ipsQuote_citation">Kari skrev:</div>
        <div class="ipsQuote_contents"><p>Hva koster den?</p></div>
      </blockquote>
      <p>Rundt&nbsp;500 kr hos <a href="https://example.com/x">Biltema</a>.</p>
      <p><img class="ipsImage" src="https://example.com/a.jpg"></p>
      <span data-excludequote><strong>Endret av Ola</strong></span>
    </div>
  </div>
</article>
<article id="elComment_11">
  <aside><h3>Gjest</h3></aside>
  <div class="ipsComment_meta"><time datetime="2020-01-02T03:04:05Z"></time></div>
  <div data-role='commentContent'><p>Takk!</p></div>
</article>
</body></html>
"""


def test_parse_topic_page() -> None:
    page = parse_topic_page(PAGE)
    assert page.title == "Bytte impeller"
    assert page.forum == "Båtforumet"
    assert page.forum_id == 1
    assert page.num_pages == 2
    assert page.expected_posts == 27

    first, second = page.posts
    assert (first.id, first.author, first.author_id) == (10, "Ola", 7)
    assert first.posted_at == "2002-12-16T18:54:04Z"
    assert first.body == (
        "Rundt 500 kr hos Biltema (https://example.com/x).\n\n"
        "[bilde: https://example.com/a.jpg]"
    )
    assert first.full_text is not None
    assert first.full_text.startswith("> Kari skrev:\n> Hva koster den?\n\nRundt 500")
    assert "Endret" not in first.full_text

    assert (second.author, second.posted_at) == ("Gjest", "2020-01-02T03:04:05Z")
    assert second.body == "Takk!"
    assert second.full_text is None


def test_page_without_posts_is_an_error() -> None:
    with pytest.raises(ParseError):
        parse_topic_page("<html><body>Vedlikehold</body></html>")


def test_parse_sitemap() -> None:
    xml = b"""<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
      <url><loc>https://baatplassen.no/i/topic/1-a/</loc><lastmod>2002</lastmod></url>
    </urlset>"""
    assert parse_sitemap(xml) == [("https://baatplassen.no/i/topic/1-a/", "2002")]
