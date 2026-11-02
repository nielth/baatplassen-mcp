package crawl

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nielth/baatplassen-mcp/internal/db"
)

// forum is a fake site. Paths map to a handler; every request is recorded.
type forum struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	requests []string
	routes   map[string]http.HandlerFunc
}

func newForum(t *testing.T) *forum {
	t.Helper()
	f := &forum{t: t, routes: map[string]http.HandlerFunc{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.Path)
		handler, ok := f.routes[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(f.server.Close)

	oldSitemap, oldWaits := sitemapURL, retryWaits
	sitemapURL = f.server.URL + "/i/sitemap.php"
	retryWaits = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { sitemapURL, retryWaits = oldSitemap, oldWaits })
	return f
}

func (f *forum) page(path, title string, numPages int, postIDs ...int) {
	articles := ""
	for _, id := range postIDs {
		articles += fmt.Sprintf(`<article id="elComment_%d">
            <div data-quoteData='{"userid":7,"username":"Ola","timestamp":1040064844}'>
            <div data-role="commentContent"><p>Innlegg %d om motor</p></div></div></article>`, id, id)
	}
	body := fmt.Sprintf(`<html><head><script type="application/ld+json">
        {"@type": "DiscussionForumPosting", "headline": %q}</script></head><body>
        <ul class="ipsPagination" data-pages="%d"></ul>%s</body></html>`, title, numPages, articles)
	f.routes[path] = func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }
}

func (f *forum) sitemaps(lastmod string, topicPaths ...string) {
	f.routes["/i/sitemap.php"] = func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
            <sitemap><loc>%s/i/sitemap.php?file=sitemap_content_forums_Topic_1</loc></sitemap>
            <sitemap><loc>%s/i/other.xml</loc></sitemap></sitemapindex>`, f.server.URL, f.server.URL)
	}
	urls := ""
	for _, path := range topicPaths {
		urls += fmt.Sprintf("<url><loc>%s%s</loc><lastmod>%s</lastmod></url>", f.server.URL, path, lastmod)
	}
	index := f.routes["/i/sitemap.php"]
	f.routes["/i/sitemap.php"] = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("file") != "" {
			fmt.Fprintf(w, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">%s</urlset>`, urls)
			return
		}
		index(w, r)
	}
}

func tempArchive(t *testing.T) *sql.DB {
	t.Helper()
	db.Path = filepath.Join(t.TempDir(), "archive.db")
	conn, err := db.Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func queue(t *testing.T, conn *sql.DB, id int, url string) {
	t.Helper()
	if err := db.UpsertTopicFromSitemap(conn, int64(id), url, nil); err != nil {
		t.Fatal(err)
	}
}

func topicState(t *testing.T, conn *sql.DB, id int) (status string, topicError *string) {
	t.Helper()
	if err := conn.QueryRow("SELECT status, error FROM topics WHERE id = ?", id).Scan(&status, &topicError); err != nil {
		t.Fatal(err)
	}
	return status, topicError
}

func count(t *testing.T, conn *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCrawlsAllPagesAndStoresPosts(t *testing.T) {
	f := newForum(t)
	f.page("/i/topic/5-a/", "Tittel", 2, 50, 51)
	f.page("/i/topic/5-a/page/2/", "Tittel", 2, 52)
	conn := tempArchive(t)
	queue(t, conn, 5, f.server.URL+"/i/topic/5-a/")

	if err := Crawl(conn, 0, -1); err != nil {
		t.Fatal(err)
	}
	if status, _ := topicState(t, conn, 5); status != "done" {
		t.Errorf("status = %s", status)
	}
	if n := count(t, conn, "SELECT COUNT(*) FROM posts WHERE topic_id = 5"); n != 3 {
		t.Errorf("posts = %d", n)
	}
	if n := count(t, conn, "SELECT COUNT(*) FROM posts_fts WHERE posts_fts MATCH 'motor'"); n != 3 {
		t.Errorf("indexed posts = %d", n)
	}
	if n := count(t, conn, "SELECT COUNT(*) FROM posts WHERE page = 2 AND id = 52"); n != 1 {
		t.Errorf("post 52 not on page 2")
	}
	var crawledAt string
	if err := conn.QueryRow("SELECT crawled_at FROM topics").Scan(&crawledAt); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(crawledAt, "+00:00") {
		t.Errorf("crawled_at = %s", crawledAt)
	}
}

func TestRecrawlingReplacesPostsWithoutDuplicates(t *testing.T) {
	f := newForum(t)
	f.page("/i/topic/5-a/", "Tittel", 1, 50)
	conn := tempArchive(t)
	queue(t, conn, 5, f.server.URL+"/i/topic/5-a/")
	for range 2 {
		if err := Crawl(conn, 0, -1); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec("UPDATE topics SET status = 'pending'"); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, conn, "SELECT COUNT(*) FROM posts_fts WHERE posts_fts MATCH 'motor'"); n != 1 {
		t.Errorf("indexed posts = %d", n)
	}
}

func TestResumesFromNextPage(t *testing.T) {
	f := newForum(t)
	f.page("/i/topic/5-a/page/3/", "Tittel", 3, 53)
	conn := tempArchive(t)
	queue(t, conn, 5, f.server.URL+"/i/topic/5-a/")
	if _, err := conn.Exec("UPDATE topics SET next_page = 3"); err != nil {
		t.Fatal(err)
	}
	if err := Crawl(conn, 0, -1); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 1 || f.requests[0] != "/i/topic/5-a/page/3/" {
		t.Errorf("requests = %v", f.requests)
	}
}

func TestProgressIsKeptWhenTheSiteFailsMidTopic(t *testing.T) {
	f := newForum(t)
	f.page("/i/topic/5-a/", "Tittel", 2, 50)
	f.routes["/i/topic/5-a/page/2/"] = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	conn := tempArchive(t)
	queue(t, conn, 5, f.server.URL+"/i/topic/5-a/")

	err := Crawl(conn, 0, -1)
	var aborted *AbortedError
	if !errors.As(err, &aborted) {
		t.Fatalf("err = %v", err)
	}
	var nextPage int
	if err := conn.QueryRow("SELECT next_page FROM topics").Scan(&nextPage); err != nil {
		t.Fatal(err)
	}
	if nextPage != 2 || count(t, conn, "SELECT COUNT(*) FROM posts") != 1 {
		t.Errorf("next_page = %d", nextPage)
	}
	if status, _ := topicState(t, conn, 5); status != "pending" {
		t.Errorf("status = %s", status)
	}
}

func TestPastTheLastPageRedirectsToIt(t *testing.T) {
	f := newForum(t)
	f.page("/i/topic/5-a/page/2/", "Tittel", 2, 52)
	f.routes["/i/topic/5-a/page/9/"] = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/i/topic/5-a/page/2/", http.StatusMovedPermanently)
	}
	conn := tempArchive(t)
	queue(t, conn, 5, f.server.URL+"/i/topic/5-a/")
	if _, err := conn.Exec("UPDATE topics SET next_page = 9"); err != nil {
		t.Fatal(err)
	}
	if err := Crawl(conn, 0, -1); err != nil {
		t.Fatal(err)
	}
	if status, _ := topicState(t, conn, 5); status != "done" {
		t.Errorf("status = %s", status)
	}
	if n := count(t, conn, "SELECT COUNT(*) FROM posts WHERE page = 2"); n != 1 {
		t.Errorf("post stored on the wrong page")
	}
}

func TestMergedTopicIsGone(t *testing.T) {
	f := newForum(t)
	f.routes["/i/topic/5-a/"] = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/i/topic/6-b/", http.StatusMovedPermanently)
	}
	f.page("/i/topic/6-b/", "Annen", 1, 60)
	conn := tempArchive(t)
	queue(t, conn, 5, f.server.URL+"/i/topic/5-a/")
	if err := Crawl(conn, 0, -1); err != nil {
		t.Fatal(err)
	}
	status, topicError := topicState(t, conn, 5)
	if status != "gone" || topicError == nil || !strings.HasPrefix(*topicError, "Redirected to ") {
		t.Errorf("status = %s, error = %v", status, topicError)
	}
	if count(t, conn, "SELECT COUNT(*) FROM posts") != 0 {
		t.Error("stored posts from the other topic")
	}
}

func TestStatusHandling(t *testing.T) {
	for _, c := range []struct {
		code int
		want string
	}{{404, "gone"}, {403, "gone"}, {410, "gone"}, {418, "error"}} {
		f := newForum(t)
		f.routes["/i/topic/5-a/"] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(c.code) }
		conn := tempArchive(t)
		queue(t, conn, 5, f.server.URL+"/i/topic/5-a/")
		if err := Crawl(conn, 0, -1); err != nil {
			t.Fatal(err)
		}
		status, topicError := topicState(t, conn, 5)
		if status != c.want || *topicError != fmt.Sprintf("HTTP %d", c.code) {
			t.Errorf("%d: status = %s, error = %v", c.code, status, topicError)
		}
	}
}

func TestRetriesThenSucceeds(t *testing.T) {
	f := newForum(t)
	f.page("/i/topic/5-a/ok", "Tittel", 1, 50)
	good := f.routes["/i/topic/5-a/ok"]
	failures := 2
	f.routes["/i/topic/5-a/"] = func(w http.ResponseWriter, r *http.Request) {
		if failures > 0 {
			failures--
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		good(w, r)
	}
	conn := tempArchive(t)
	queue(t, conn, 5, f.server.URL+"/i/topic/5-a/")
	if err := Crawl(conn, 0, -1); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 3 {
		t.Errorf("requests = %v", f.requests)
	}
	if status, _ := topicState(t, conn, 5); status != "done" {
		t.Errorf("status = %s", status)
	}
}

func TestRequestsAreSpacedOut(t *testing.T) {
	f := newForum(t)
	f.page("/i/topic/5-a/", "Tittel", 3, 50)
	f.page("/i/topic/5-a/page/2/", "Tittel", 3, 51)
	f.page("/i/topic/5-a/page/3/", "Tittel", 3, 52)
	conn := tempArchive(t)
	queue(t, conn, 5, f.server.URL+"/i/topic/5-a/")
	started := time.Now()
	if err := Crawl(conn, 100*time.Millisecond, -1); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 200*time.Millisecond {
		t.Errorf("three requests took only %v", elapsed)
	}
}

func TestUserAgent(t *testing.T) {
	f := newForum(t)
	var agent string
	f.routes["/i/topic/5-a/"] = func(_ http.ResponseWriter, r *http.Request) { agent = r.Header.Get("User-Agent") }
	conn := tempArchive(t)
	queue(t, conn, 5, f.server.URL+"/i/topic/5-a/")
	if err := Crawl(conn, 0, 1); err != nil {
		t.Fatal(err)
	}
	if agent != UserAgent {
		t.Errorf("agent = %q", agent)
	}
}

func TestUnparseablePageStopsAfterTwentyInARow(t *testing.T) {
	f := newForum(t)
	conn := tempArchive(t)
	for id := 1; id <= 25; id++ {
		path := fmt.Sprintf("/i/topic/%d-a/", id)
		f.routes[path] = func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "<html>Vedlikehold</html>") }
		queue(t, conn, id, f.server.URL+path)
	}
	err := Crawl(conn, 0, -1)
	var aborted *AbortedError
	if !errors.As(err, &aborted) {
		t.Fatalf("err = %v", err)
	}
	if n := count(t, conn, "SELECT COUNT(*) FROM topics WHERE status = 'error'"); n != 20 {
		t.Errorf("errored topics = %d", n)
	}
}

func TestMaxTopicsNewestFirst(t *testing.T) {
	f := newForum(t)
	f.page("/i/topic/1-a/", "Eldst", 1, 10)
	f.page("/i/topic/2-b/", "Nyest", 1, 20)
	conn := tempArchive(t)
	queue(t, conn, 1, f.server.URL+"/i/topic/1-a/")
	queue(t, conn, 2, f.server.URL+"/i/topic/2-b/")
	if err := Crawl(conn, 0, 1); err != nil {
		t.Fatal(err)
	}
	if status, _ := topicState(t, conn, 2); status != "done" {
		t.Errorf("newest = %s", status)
	}
	if status, _ := topicState(t, conn, 1); status != "pending" {
		t.Errorf("oldest = %s", status)
	}
}

func TestDiscoverQueuesTopicsAndSkipsExcluded(t *testing.T) {
	f := newForum(t)
	f.sitemaps("2020", "/i/topic/5-a/", "/i/topic/179874-excluded/", "/i/not-a-topic/")
	conn := tempArchive(t)
	if err := Discover(conn, 0); err != nil {
		t.Fatal(err)
	}
	if n := count(t, conn, "SELECT COUNT(*) FROM topics WHERE status = 'pending' AND lastmod = '2020' AND id = 5"); n != 1 {
		t.Error("topic 5 not queued")
	}
	if n := count(t, conn, "SELECT COUNT(*) FROM topics"); n != 1 {
		t.Errorf("topics = %d", n)
	}
	for _, request := range f.requests {
		if strings.Contains(request, "other.xml") {
			t.Error("read a sitemap that isn't for topics")
		}
	}
}

func TestDiscoverWithoutTopicSitemapsAborts(t *testing.T) {
	f := newForum(t)
	f.routes["/i/sitemap.php"] = func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"></sitemapindex>`)
	}
	var aborted *AbortedError
	if err := Discover(tempArchive(t), 0); !errors.As(err, &aborted) {
		t.Errorf("err = %v", err)
	}
}

// discoveries counts how many times the sitemap index has been read.
func discoveries(f *forum) int {
	n := 0
	for _, request := range f.requests {
		if request == "/i/sitemap.php" {
			n++
		}
	}
	return n / 2 // Every discovery reads the index and one topic sitemap, at the same path.
}

const day = float64(DiscoverInterval / time.Second)

func TestDiscoversFirstAndThenCrawls(t *testing.T) {
	f := newForum(t)
	f.sitemaps("2020", "/i/topic/5-a/")
	f.page("/i/topic/5-a/", "Tittel", 1, 50)
	conn := tempArchive(t)
	wait, err := runOnce(conn, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if discoveries(f) != 1 || wait != DiscoverInterval {
		t.Errorf("discoveries = %d, wait = %v", discoveries(f), wait)
	}
	if status, _ := topicState(t, conn, 5); status != "done" {
		t.Errorf("status = %s", status)
	}
}

func TestRestartWithinADayOnlyCrawls(t *testing.T) {
	f := newForum(t)
	f.sitemaps("2020", "/i/topic/5-a/")
	f.page("/i/topic/5-a/", "Tittel", 1, 50)
	conn := tempArchive(t)
	if _, err := runOnce(conn, 0, 1000); err != nil {
		t.Fatal(err)
	}
	wait, err := runOnce(conn, 0, 1000+3600)
	if err != nil {
		t.Fatal(err)
	}
	if discoveries(f) != 1 || wait != DiscoverInterval-time.Hour {
		t.Errorf("discoveries = %d, wait = %v", discoveries(f), wait)
	}
}

func TestDiscoversAgainAfterADay(t *testing.T) {
	f := newForum(t)
	f.sitemaps("2020", "/i/topic/5-a/")
	f.page("/i/topic/5-a/", "Tittel", 1, 50)
	conn := tempArchive(t)
	if _, err := runOnce(conn, 0, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := runOnce(conn, 0, 1000+day); err != nil {
		t.Fatal(err)
	}
	if discoveries(f) != 2 {
		t.Errorf("discoveries = %d", discoveries(f))
	}
}

func TestNewPostsInAChangedTopicAreFetched(t *testing.T) {
	f := newForum(t)
	f.sitemaps("2020", "/i/topic/5-a/")
	f.page("/i/topic/5-a/", "Tittel", 2, 50)
	f.page("/i/topic/5-a/page/2/", "Tittel", 2, 51)
	conn := tempArchive(t)
	if _, err := runOnce(conn, 0, 1000); err != nil {
		t.Fatal(err)
	}
	f.sitemaps("2021", "/i/topic/5-a/")
	f.page("/i/topic/5-a/page/2/", "Tittel", 2, 51, 52)
	f.requests = nil
	if _, err := runOnce(conn, 0, 1000+day); err != nil {
		t.Fatal(err)
	}
	if n := count(t, conn, "SELECT COUNT(*) FROM posts"); n != 3 {
		t.Errorf("posts = %d", n)
	}
	for _, request := range f.requests {
		if request == "/i/topic/5-a/" {
			t.Error("re-read the first page of a changed topic")
		}
	}
}

func TestFailedDiscoveryIsRetriedNextTime(t *testing.T) {
	f := newForum(t)
	f.routes["/i/sitemap.php"] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }
	conn := tempArchive(t)
	_, err := runOnce(conn, 0, 1000)
	var aborted *AbortedError
	if !errors.As(err, &aborted) {
		t.Fatalf("err = %v", err)
	}
	if _, found, err := lastDiscovery(conn); found || err != nil {
		t.Errorf("found = %v, err = %v", found, err)
	}
}

func TestReadsALastDiscoveryWrittenByTheOldCrawler(t *testing.T) {
	conn := tempArchive(t)
	if _, err := conn.Exec("INSERT INTO meta (key, value) VALUES ('last_discovery', '1762088288.1234567')"); err != nil {
		t.Fatal(err)
	}
	last, found, err := lastDiscovery(conn)
	if err != nil || !found || last != 1762088288.1234567 {
		t.Errorf("last = %v, found = %v, err = %v", last, found, err)
	}
}

func TestOnlyOneCrawlerCanHoldTheLock(t *testing.T) {
	db.Path = filepath.Join(t.TempDir(), "archive.db")
	lock, err := AcquireLock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(); err == nil {
		t.Fatal("second crawler got the lock")
	}
	lock.Close()
	again, err := AcquireLock()
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
}
