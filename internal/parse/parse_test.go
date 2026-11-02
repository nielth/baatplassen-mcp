package parse

import (
	"errors"
	"strings"
	"testing"
)

const page = `
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
`

func TestParseTopicPage(t *testing.T) {
	result, err := ParseTopicPage(page)
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "Bytte impeller" || *result.Forum != "Båtforumet" || *result.ForumID != 1 {
		t.Errorf("topic = %q %q %d", result.Title, *result.Forum, *result.ForumID)
	}
	if result.NumPages != 2 || *result.ExpectedPosts != 27 {
		t.Errorf("pages = %d, expected posts = %d", result.NumPages, *result.ExpectedPosts)
	}

	first, second := result.Posts[0], result.Posts[1]
	if first.ID != 10 || first.Author != "Ola" || *first.AuthorID != 7 {
		t.Errorf("first post = %d %q %d", first.ID, first.Author, *first.AuthorID)
	}
	if *first.PostedAt != "2002-12-16T18:54:04Z" {
		t.Errorf("posted at = %q", *first.PostedAt)
	}
	wantBody := "Rundt 500 kr hos Biltema (https://example.com/x).\n\n[bilde: https://example.com/a.jpg]"
	if first.Body != wantBody {
		t.Errorf("body = %q", first.Body)
	}
	if first.FullText == nil || !strings.HasPrefix(*first.FullText, "> Kari skrev:\n> Hva koster den?\n\nRundt 500") {
		t.Errorf("full text = %v", first.FullText)
	}
	if strings.Contains(*first.FullText, "Endret") {
		t.Errorf("edit notice kept: %q", *first.FullText)
	}

	if second.Author != "Gjest" || *second.PostedAt != "2020-01-02T03:04:05Z" {
		t.Errorf("second post = %q %q", second.Author, *second.PostedAt)
	}
	if second.Body != "Takk!" || second.FullText != nil {
		t.Errorf("second body = %q, full text = %v", second.Body, second.FullText)
	}
}

func TestPageWithoutPostsIsAnError(t *testing.T) {
	_, err := ParseTopicPage("<html><body>Vedlikehold</body></html>")
	var parseError *ParseError
	if !errors.As(err, &parseError) {
		t.Fatalf("err = %v", err)
	}
}

func TestEmoticonsBecomeTheirAltText(t *testing.T) {
	result, err := ParseTopicPage(strings.Replace(page, "<p>Takk!</p>",
		`<p>Takk <img src="https://x/emoticons/smile.png" alt=":)"> <img class="a ipsEmoji" src="https://x/e.png" alt="😀"></p>`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Posts[1].Body; got != "Takk :) 😀" {
		t.Errorf("body = %q", got)
	}
}

func TestNestedQuotes(t *testing.T) {
	result, err := ParseTopicPage(strings.Replace(page, "<p>Takk!</p>", `
        <blockquote><div class="ipsQuote_citation">A skrev:</div>
          <blockquote><div class="ipsQuote_citation">B skrev:</div><p>Inni</p></blockquote>
          <p>Ute</p>
        </blockquote>
        <p>Svar</p>`, 1))
	if err != nil {
		t.Fatal(err)
	}
	post := result.Posts[1]
	if post.Body != "Svar" {
		t.Errorf("body = %q", post.Body)
	}
	want := "> A skrev:\n> > B skrev:\n> > Inni\n> Ute\n\nSvar"
	if *post.FullText != want {
		t.Errorf("full text = %q, want %q", *post.FullText, want)
	}
}

func TestUnicodeWhitespaceIsCollapsedAndStripped(t *testing.T) {
	result, err := ParseTopicPage(strings.Replace(page, "<p>Takk!</p>", "<p>　 Takk  for hjelpen\u0085</p>", 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Posts[1].Body; got != "Takk for hjelpen" {
		t.Errorf("body = %q", got)
	}
}

func TestEscapeControlChars(t *testing.T) {
	got := escapeControlChars("{\"a\": \"x\ny\\\"z\tq\",\n \"b\": 1}")
	want := "{\"a\": \"x\\u000ay\\\"z\\u0009q\",\n \"b\": 1}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseSitemap(t *testing.T) {
	entries, err := ParseSitemap([]byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
      <url><loc> https://baatplassen.no/i/topic/1-a/ </loc><lastmod>2002</lastmod></url>
      <url><loc>https://baatplassen.no/i/topic/2-b/</loc></url>
      <url><other>ignored</other></url>
    </urlset>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Loc != "https://baatplassen.no/i/topic/1-a/" || *entries[0].Lastmod != "2002" {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[1].Lastmod != nil {
		t.Errorf("lastmod = %q", *entries[1].Lastmod)
	}
}
