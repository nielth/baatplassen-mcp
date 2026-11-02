// Package crawl is a polite, resumable crawler: one request at a time, progress committed
// after each page.
package crawl

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nielth/baatplassen-mcp/internal/db"
	"github.com/nielth/baatplassen-mcp/internal/parse"
)

// UserAgent identifies the crawler to the forum.
const UserAgent = "baatplassen-personal-archive/0.1"

const (
	maxConsecutiveParseErrors = 20
	// DiscoverInterval is how long to wait between looking for new posts.
	DiscoverInterval   = 24 * time.Hour
	retryAfterFailure  = time.Hour
	requestTimeout     = 60 * time.Second
	maxRedirects       = 20
	topicSitemapMarker = "sitemap_content_forums_Topic"
)

var (
	// Variables so that tests can run against a local server without waiting.
	sitemapURL = "https://baatplassen.no/i/sitemap.php"
	retryWaits = []time.Duration{
		30 * time.Second, 60 * time.Second, 120 * time.Second,
		300 * time.Second, 600 * time.Second, 900 * time.Second,
	}

	// Disallowed by name in https://baatplassen.no/robots.txt
	robotsExcludedTopics = map[int64]bool{179874: true, 179667: true}

	goneStatuses  = map[int]bool{403: true, 404: true, 410: true}
	retryStatuses = map[int]bool{
		408: true, 425: true, 429: true, 500: true, 502: true,
		503: true, 504: true, 520: true, 522: true, 524: true,
	}

	topicIDPattern = regexp.MustCompile(`/topic/([0-9]+)-`)
	pagePattern    = regexp.MustCompile(`/page/([0-9]+)/`)
)

// AbortedError means the site looks unhealthy or has changed. Rerunning resumes where this
// stopped.
type AbortedError struct{ Message string }

func (e *AbortedError) Error() string { return e.Message }

func logf(format string, args ...any) {
	fmt.Printf("%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// fetcher is an HTTP client that spaces requests out and backs off when the server struggles.
type fetcher struct {
	delay       time.Duration
	lastRequest time.Time
	requests    int
	client      *http.Client
}

type response struct {
	status   int
	finalURL string
	body     []byte
	header   http.Header
}

func newFetcher(delay time.Duration) *fetcher {
	return &fetcher{
		delay: delay,
		client: &http.Client{
			Timeout: requestTimeout,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}
}

func (f *fetcher) fetchOnce(url string) (*response, error) {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", UserAgent)
	request.Header.Set("Accept", "*/*")
	result, err := f.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer result.Body.Close()
	body, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, err
	}
	return &response{
		status:   result.StatusCode,
		finalURL: result.Request.URL.String(),
		body:     body,
		header:   result.Header,
	}, nil
}

func (f *fetcher) get(url string) (*response, error) {
	for attempt := 0; ; attempt++ {
		time.Sleep(time.Until(f.lastRequest.Add(f.delay)))
		f.lastRequest = time.Now()
		f.requests++

		var problem string
		var retryAfter time.Duration
		result, err := f.fetchOnce(url)
		if err != nil {
			problem = err.Error()
		} else {
			if !retryStatuses[result.status] {
				return result, nil
			}
			problem = fmt.Sprintf("HTTP %d", result.status)
			if seconds, err := strconv.Atoi(result.header.Get("Retry-After")); err == nil && seconds > 0 {
				retryAfter = time.Duration(seconds) * time.Second
			}
		}
		if attempt >= len(retryWaits) {
			return nil, &AbortedError{Message: fmt.Sprintf("Giving up on %s: %s", url, problem)}
		}
		pause := max(retryWaits[attempt], retryAfter)
		logf("%s for %s, waiting %.0fs", problem, url, pause.Seconds())
		time.Sleep(pause)
	}
}

func (f *fetcher) getSitemap(url string) ([]parse.SitemapEntry, error) {
	result, err := f.get(url)
	if err != nil {
		return nil, err
	}
	if result.status < 200 || result.status >= 300 {
		return nil, &AbortedError{Message: fmt.Sprintf("HTTP %d for %s", result.status, url)}
	}
	entries, err := parse.ParseSitemap(result.body)
	if err != nil {
		return nil, &AbortedError{Message: fmt.Sprintf("Could not read %s: %v", url, err)}
	}
	return entries, nil
}

func topicIDFromURL(url string) (int64, bool) {
	match := topicIDPattern.FindStringSubmatch(url)
	if match == nil {
		return 0, false
	}
	id, err := strconv.ParseInt(match[1], 10, 64)
	return id, err == nil
}

// Discover reads every topic sitemap and queues new or changed topics.
func Discover(conn *sql.DB, delay time.Duration) error {
	f := newFetcher(delay)
	index, err := f.getSitemap(sitemapURL)
	if err != nil {
		return err
	}
	sitemaps := []string{}
	for _, entry := range index {
		if strings.Contains(entry.Loc, topicSitemapMarker) {
			sitemaps = append(sitemaps, entry.Loc)
		}
	}
	if len(sitemaps) == 0 {
		return &AbortedError{Message: "No topic sitemaps found in the sitemap index"}
	}
	logf("Reading %d topic sitemaps", len(sitemaps))
	for number, sitemap := range sitemaps {
		entries, err := f.getSitemap(sitemap)
		if err != nil {
			return err
		}
		if err := queueEntries(conn, entries); err != nil {
			return err
		}
		if (number+1)%25 == 0 {
			logf("Read %d/%d sitemaps", number+1, len(sitemaps))
		}
	}
	var pending, total int
	if err := conn.QueryRow("SELECT COUNT(*) FROM topics WHERE status = 'pending'").Scan(&pending); err != nil {
		return err
	}
	if err := conn.QueryRow("SELECT COUNT(*) FROM topics").Scan(&total); err != nil {
		return err
	}
	logf("%d topics known, %d to crawl", total, pending)
	return nil
}

func queueEntries(conn *sql.DB, entries []parse.SitemapEntry) error {
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, entry := range entries {
		id, ok := topicIDFromURL(entry.Loc)
		if !ok || robotsExcludedTopics[id] {
			continue
		}
		if err := db.UpsertTopicFromSitemap(tx, id, entry.Loc, entry.Lastmod); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func finishTopic(conn db.Execer, topicID int64, status string, topicError *string) error {
	_, err := conn.Exec(
		"UPDATE topics SET status = ?, error = ?, crawled_at = ? WHERE id = ?",
		status, topicError, time.Now().UTC().Format("2006-01-02T15:04:05+00:00"), topicID,
	)
	return err
}

// saveProgress stores a page together with what to do next, so the two can't disagree.
func saveProgress(conn *sql.DB, topicID int64, actualPage int, page *parse.TopicPage) error {
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := db.SavePage(tx, topicID, actualPage, page); err != nil {
		return err
	}
	if actualPage >= page.NumPages {
		err = finishTopic(tx, topicID, "done", nil)
	} else {
		_, err = tx.Exec("UPDATE topics SET next_page = ? WHERE id = ?", actualPage+1, topicID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// crawlTopic crawls a topic from the given page. Returns false if a page could not be parsed.
func crawlTopic(conn *sql.DB, f *fetcher, topicID int64, topicURL string, start int) (bool, error) {
	base := strings.TrimRight(topicURL, "/") + "/"
	pageNumber := start
	for {
		pageURL := base
		if pageNumber != 1 {
			pageURL = fmt.Sprintf("%spage/%d/", base, pageNumber)
		}
		result, err := f.get(pageURL)
		if err != nil {
			return false, err
		}
		if goneStatuses[result.status] {
			message := fmt.Sprintf("HTTP %d", result.status)
			return true, finishTopic(conn, topicID, "gone", &message)
		}
		if result.status != http.StatusOK {
			message := fmt.Sprintf("HTTP %d", result.status)
			return true, finishTopic(conn, topicID, "error", &message)
		}

		if id, ok := topicIDFromURL(result.finalURL); !ok || id != topicID {
			// Merged into another topic, which has its own sitemap entry.
			message := "Redirected to " + result.finalURL
			return true, finishTopic(conn, topicID, "gone", &message)
		}
		// Asking past the last page redirects to the last page.
		actualPage := 1
		if found := pagePattern.FindStringSubmatch(result.finalURL); found != nil {
			if actualPage, err = strconv.Atoi(found[1]); err != nil {
				return false, err
			}
		}

		page, err := parse.ParseTopicPage(string(result.body))
		if err != nil {
			var parseError *parse.ParseError
			if !errors.As(err, &parseError) {
				return false, err
			}
			message := pageURL + ": " + err.Error()
			if err := finishTopic(conn, topicID, "error", &message); err != nil {
				return false, err
			}
			logf("Could not parse %s: %v", pageURL, err)
			return false, nil
		}

		if err := saveProgress(conn, topicID, actualPage, page); err != nil {
			return false, err
		}
		if actualPage >= page.NumPages {
			return true, nil
		}
		pageNumber = actualPage + 1
	}
}

// Crawl crawls queued topics, newest first, until none are left. A negative maxTopics means
// no limit.
func Crawl(conn *sql.DB, delay time.Duration, maxTopics int) error {
	f := newFetcher(delay)
	started := time.Now()
	crawled, parseErrors := 0, 0
	for maxTopics < 0 || crawled < maxTopics {
		var id int64
		var url string
		var nextPage int
		err := conn.QueryRow(`
            SELECT id, url, next_page FROM topics
            WHERE status = 'pending' ORDER BY id DESC LIMIT 1
            `).Scan(&id, &url, &nextPage)
		if errors.Is(err, sql.ErrNoRows) {
			logf("Nothing left to crawl")
			break
		}
		if err != nil {
			return err
		}
		parsed, err := crawlTopic(conn, f, id, url, nextPage)
		if err != nil {
			return err
		}
		if parsed {
			parseErrors = 0
		} else {
			parseErrors++
			if parseErrors >= maxConsecutiveParseErrors {
				return &AbortedError{Message: fmt.Sprintf(
					"%d unparseable topics in a row. The site may be blocking the crawler or have changed its layout.",
					parseErrors,
				)}
			}
		}
		crawled++
		if crawled%100 == 0 {
			var remaining int
			if err := conn.QueryRow("SELECT COUNT(*) FROM topics WHERE status = 'pending'").Scan(&remaining); err != nil {
				return err
			}
			rate := float64(f.requests) / time.Since(started).Seconds()
			logf("%d topics, %d requests this run (%.2f/s), %d topics left", crawled, f.requests, rate, remaining)
		}
	}
	logf("Stopped after %d topics and %d requests", crawled, f.requests)
	return nil
}

// AcquireLock makes sure only one crawler runs. The lock lasts as long as the returned file
// stays open. It is a plain flock, so it also excludes a crawler from another program.
func AcquireLock() (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(db.Path), 0o755); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(db.Path), "crawl.lock"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another crawler is already running")
	}
	return lock, nil
}

// lastDiscovery returns when the sitemaps were last read, in Unix seconds, if ever.
func lastDiscovery(conn *sql.DB) (float64, bool, error) {
	var value string
	err := conn.QueryRow("SELECT value FROM meta WHERE key = 'last_discovery'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	last, err := strconv.ParseFloat(value, 64)
	return last, err == nil, err
}

// runOnce discovers if due, crawls everything queued, and returns the time until next time.
func runOnce(conn *sql.DB, delay time.Duration, now float64) (time.Duration, error) {
	last, found, err := lastDiscovery(conn)
	if err != nil {
		return 0, err
	}
	if !found || now-last >= DiscoverInterval.Seconds() {
		if err := Discover(conn, delay); err != nil {
			return 0, err
		}
		last = now
		if _, err := conn.Exec(
			"INSERT OR REPLACE INTO meta (key, value) VALUES ('last_discovery', ?)",
			strconv.FormatFloat(last, 'f', -1, 64),
		); err != nil {
			return 0, err
		}
	}
	if err := Crawl(conn, delay, -1); err != nil {
		return 0, err
	}
	wait := math.Max(0, last+DiscoverInterval.Seconds()-now)
	return time.Duration(wait * float64(time.Second)), nil
}

// RunForever keeps the archive complete and up to date, surviving site outages. It only
// returns on a problem that isn't the site's fault, such as a database error.
func RunForever(conn *sql.DB, delay time.Duration) error {
	for {
		wait, err := runOnce(conn, delay, float64(time.Now().UnixMilli())/1000)
		var aborted *AbortedError
		switch {
		case err == nil:
			logf("Up to date. Next check for new posts in %.1f hours", wait.Hours())
		case errors.As(err, &aborted):
			wait = retryAfterFailure
			logf("Crawl failed: %v. Trying again in %.1f hours", err, wait.Hours())
		default:
			return err
		}
		time.Sleep(wait)
	}
}
