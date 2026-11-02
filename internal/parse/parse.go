// Package parse reads Invision Community topic pages and sitemaps from baatplassen.no.
package parse

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// SpaceClass is every character Unicode treats as whitespace, as a regexp character class
// body. Go's \s only covers ASCII, and forum posts contain the rest.
const SpaceClass = `\t\n\x0b\f\r \x1c-\x1f\x85\xa0\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

var (
	spaceRun       = regexp.MustCompile("[" + SpaceClass + "]+")
	manyNewlines   = regexp.MustCompile(`\n{3,}`)
	commentID      = regexp.MustCompile(`^elComment_([0-9]+)$`)
	forumIDPattern = regexp.MustCompile(`/forum/([0-9]+)-`)
	blockTags      = map[string]bool{
		"p": true, "div": true, "ul": true, "ol": true, "li": true, "table": true, "tr": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "pre": true,
	}
	skipTags = map[string]bool{"script": true, "style": true, "noscript": true}
)

// ParseError means the page did not look like a topic page.
type ParseError struct{ Message string }

func (e *ParseError) Error() string { return e.Message }

// Post is one forum post.
type Post struct {
	ID       int64
	Author   string
	AuthorID *int64
	PostedAt *string
	Body     string  // The poster's own words: quotes removed. This is what gets indexed.
	FullText *string // Body with quotes inline. Nil when the post has no quotes.
}

// TopicPage is one page of a topic.
type TopicPage struct {
	Title         string
	ForumID       *int64
	Forum         *string
	NumPages      int
	ExpectedPosts *int
	Posts         []Post
}

// SitemapEntry is one line of a sitemap or sitemap index.
type SitemapEntry struct {
	Loc     string
	Lastmod *string
}

// ParseSitemap returns the entries of a sitemap or sitemap index.
func ParseSitemap(data []byte) ([]SitemapEntry, error) {
	var doc struct {
		Items []struct {
			Loc     *string `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 loc"`
			Lastmod *string `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 lastmod"`
		} `xml:",any"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("sitemap: %w", err)
	}
	entries := []SitemapEntry{}
	for _, item := range doc.Items {
		if item.Loc != nil && *item.Loc != "" {
			entries = append(entries, SitemapEntry{Loc: TrimSpace(*item.Loc), Lastmod: item.Lastmod})
		}
	}
	return entries, nil
}

// IsSpace reports whether r is Unicode whitespace, including the control characters
// strings.TrimSpace leaves alone.
func IsSpace(r rune) bool {
	switch {
	case r >= 0x1c && r <= 0x1f, r >= 0x09 && r <= 0x0d, r == ' ', r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

// TrimSpace removes whitespace, as defined by IsSpace, from both ends.
func TrimSpace(s string) string {
	return strings.TrimFunc(s, IsSpace)
}

func attr(node *html.Node, name string) (string, bool) {
	for _, a := range node.Attr {
		if a.Key == name {
			return a.Val, true
		}
	}
	return "", false
}

func hasClass(node *html.Node, class string) bool {
	value, _ := attr(node, "class")
	for _, name := range strings.FieldsFunc(value, IsSpace) {
		if name == class {
			return true
		}
	}
	return false
}

// firstOf returns the first non-empty attribute of the node.
func firstOf(node *html.Node, names ...string) string {
	for _, name := range names {
		if value, _ := attr(node, name); value != "" {
			return value
		}
	}
	return ""
}

func renderChildren(node *html.Node, keepQuotes bool) string {
	var text strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		text.WriteString(render(child, keepQuotes))
	}
	return text.String()
}

// render converts a post's HTML to plain text, keeping links and image URLs.
func render(node *html.Node, keepQuotes bool) string {
	if node.Type == html.TextNode {
		return spaceRun.ReplaceAllString(strings.ReplaceAll(node.Data, " ", " "), " ")
	}
	if node.Type != html.ElementNode || skipTags[node.Data] {
		return ""
	}
	if _, excluded := attr(node, "data-excludequote"); excluded { // "Endret ... av ..." edit notice
		return ""
	}
	switch node.Data {
	case "br":
		return "\n"
	case "img":
		source := firstOf(node, "src", "data-src")
		if strings.Contains(source, "/emoticons/") || hasClass(node, "ipsEmoji") {
			return firstOf(node, "alt")
		}
		return "[bilde: " + source + "]"
	case "blockquote":
		if !keepQuotes {
			return ""
		}
		return renderQuote(node)
	}

	inner := renderChildren(node, keepQuotes)
	if node.Data == "a" {
		href := firstOf(node, "href")
		text := TrimSpace(inner)
		if strings.HasPrefix(href, "http") && text != "" && !strings.Contains(text, "[bilde:") && text != href {
			return inner + " (" + href + ")"
		}
	}
	if blockTags[node.Data] {
		return "\n" + inner + "\n"
	}
	return inner
}

func renderQuote(node *html.Node) string {
	header := "Sitat:"
	citation := goquery.NewDocumentFromNode(node).Find(".ipsQuote_citation").First()
	if len(citation.Nodes) > 0 {
		found := citation.Nodes[0]
		found.Parent.RemoveChild(found)
		header = clean(allText(found))
	}
	lines := []string{"> " + header}
	for _, line := range strings.Split(clean(renderChildren(node, true)), "\n") {
		if TrimSpace(line) != "" {
			lines = append(lines, "> "+line)
		}
	}
	return "\n" + strings.Join(lines, "\n") + "\n\n"
}

// clean strips every line and leaves at most one blank line in a row.
func clean(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = TrimSpace(line)
	}
	return TrimSpace(manyNewlines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// allText returns the text below a node, without scripts and styles.
func allText(node *html.Node) string {
	if node.Type == html.TextNode {
		return node.Data
	}
	if node.Type != html.ElementNode || node.Data == "script" || node.Data == "style" {
		return ""
	}
	var text strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		text.WriteString(allText(child))
	}
	return text.String()
}

// strippedText joins the text below a node with every piece stripped of whitespace.
func strippedText(node *html.Node) string {
	if node.Type == html.TextNode {
		return TrimSpace(node.Data)
	}
	if node.Type != html.ElementNode || node.Data == "script" || node.Data == "style" {
		return ""
	}
	var text strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		text.WriteString(strippedText(child))
	}
	return text.String()
}

func htmlToText(content *html.Node, keepQuotes bool) string {
	return clean(renderChildren(content, keepQuotes))
}

// escapeControlChars makes JSON with raw control characters inside strings valid. The
// site's JSON-LD has raw newlines in post text, which encoding/json rejects.
func escapeControlChars(data string) string {
	var out strings.Builder
	inString, escaped := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case !inString:
			inString = c == '"'
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case c == '"':
			inString = false
		case c < 0x20:
			fmt.Fprintf(&out, `\u%04x`, c)
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

// toInt converts a JSON number or numeric string. The forum's JSON uses both.
func toInt(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), true
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil
	}
	return 0, false
}

// topicMetadata returns the title and reply count from the page's JSON-LD.
func topicMetadata(doc *goquery.Document) (*string, *int) {
	var title *string
	var replies *int
	doc.Find(`script[type="application/ld+json"]`).EachWithBreak(func(_ int, script *goquery.Selection) bool {
		var data map[string]any
		if json.Unmarshal([]byte(escapeControlChars(script.Text())), &data) != nil {
			return true
		}
		if data["@type"] != "DiscussionForumPosting" {
			return true
		}
		stats, _ := data["interactionStatistic"].([]any)
		for _, item := range stats {
			stat, _ := item.(map[string]any)
			kind, _ := stat["interactionType"].(string)
			if strings.HasSuffix(kind, "CommentAction") {
				if count, ok := toInt(stat["userInteractionCount"]); ok {
					n := int(count)
					replies = &n
				}
			}
		}
		if headline, ok := data["headline"].(string); ok {
			title = &headline
		}
		return false
	})
	return title, replies
}

func parsePost(article *goquery.Selection) (Post, bool) {
	id, _ := article.Attr("id")
	match := commentID.FindStringSubmatch(id)
	content := article.Find(`[data-role="commentContent"]`).First()
	if match == nil || len(content.Nodes) == 0 {
		return Post{}, false
	}
	postID, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return Post{}, false
	}
	post := Post{ID: postID}

	quoteData := map[string]any{}
	if value, ok := article.Find("[data-quotedata]").First().Attr("data-quotedata"); ok {
		if json.Unmarshal([]byte(value), &quoteData) != nil {
			quoteData = map[string]any{}
		}
	}

	post.Author, _ = quoteData["username"].(string)
	if post.Author == "" {
		heading := article.Find("aside h3").First()
		if len(heading.Nodes) == 0 {
			heading = article.Find("h3").First()
		}
		post.Author = "Ukjent"
		if len(heading.Nodes) > 0 {
			post.Author = strippedText(heading.Nodes[0])
		}
	}

	if timestamp, ok := toInt(quoteData["timestamp"]); ok && timestamp != 0 {
		formatted := time.Unix(timestamp, 0).UTC().Format("2006-01-02T15:04:05Z")
		post.PostedAt = &formatted
	} else if value, ok := article.Find(".ipsComment_meta time[datetime]").First().Attr("datetime"); ok {
		post.PostedAt = &value
	}

	if userID, ok := toInt(quoteData["userid"]); ok && userID != 0 {
		post.AuthorID = &userID
	}

	hasQuotes := content.Find("blockquote").Length() > 0
	post.Body = htmlToText(content.Nodes[0], false)
	if hasQuotes {
		// Rendering quotes removes their citations from the tree, so this comes last.
		fullText := htmlToText(content.Nodes[0], true)
		post.FullText = &fullText
	}
	return post, true
}

// ParseTopicPage reads one page of a topic.
func ParseTopicPage(page string) (*TopicPage, error) {
	if !utf8.ValidString(page) {
		page = strings.ToValidUTF8(page, "�")
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		return nil, &ParseError{Message: err.Error()}
	}
	title, replies := topicMetadata(doc)
	articles := doc.Find(`article[id^="elComment_"]`)
	posts := []Post{}
	articles.Each(func(_ int, article *goquery.Selection) {
		if post, ok := parsePost(article); ok {
			posts = append(posts, post)
		}
	})
	if title == nil || len(posts) == 0 || len(posts) != articles.Length() {
		shown := "None"
		if title != nil {
			shown = strconv.Quote(*title)
		}
		return nil, &ParseError{Message: fmt.Sprintf("title=%s, articles=%d, parsed=%d", shown, articles.Length(), len(posts))}
	}

	result := &TopicPage{Title: *title, NumPages: 1, Posts: posts}
	links := doc.Find(`[data-role="breadcrumbList"]`).First().Find(`a[href*="/forum/"]`)
	if links.Length() > 0 {
		last := links.Last()
		forum := strippedText(last.Nodes[0])
		result.Forum = &forum
		href, _ := last.Attr("href")
		if found := forumIDPattern.FindStringSubmatch(href); found != nil {
			if id, err := strconv.ParseInt(found[1], 10, 64); err == nil {
				result.ForumID = &id
			}
		}
	}

	if pages, ok := doc.Find("ul.ipsPagination[data-pages]").First().Attr("data-pages"); ok {
		numPages, err := strconv.Atoi(pages)
		if err != nil {
			return nil, &ParseError{Message: fmt.Sprintf("bad page count %q", pages)}
		}
		result.NumPages = numPages
	}
	if replies != nil {
		expected := *replies + 1
		result.ExpectedPosts = &expected
	}
	return result, nil
}
