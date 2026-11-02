// Package images downloads post images on demand and caches them, resized for Claude, in SQLite.
package images

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/bmp"  // Registers the BMP decoder.
	_ "golang.org/x/image/webp" // Registers the WebP decoder.

	"github.com/nielth/baatplassen-mcp/internal/crawl"
	"github.com/nielth/baatplassen-mcp/internal/db"
	"github.com/nielth/baatplassen-mcp/internal/parse"
)

const (
	maxDownloadBytes = 25_000_000
	maxSidePixels    = 1568 // Larger images are scaled down by Claude anyway.
	jpegQuality      = 85
	maxPixels        = 178_956_970 // Decoding a larger image could use gigabytes of memory.
	downloadTimeout  = 5 * time.Minute
)

var (
	siteHosts      = map[string]bool{"baatplassen.no": true, "www.baatplassen.no": true}
	allowedFormats = map[string]bool{"jpeg": true, "png": true, "gif": true, "webp": true, "bmp": true}
	imageMarker    = regexp.MustCompile(`\[bilde: ([^\]` + parse.SpaceClass + `]+)\]`)
)

// UnavailableError means the image can't be shown. The message says why.
type UnavailableError struct{ Reason string }

func (e *UnavailableError) Error() string { return e.Reason }

// URLs returns the image URLs in a post's text.
func URLs(text string) []string {
	urls := []string{}
	for _, match := range imageMarker.FindAllStringSubmatch(text, -1) {
		urls = append(urls, match[1])
	}
	return urls
}

// refusalReason says why a URL must not be fetched, or returns "" if it may be.
//
// The URLs come from posts written by strangers, so only the forum's own uploads are
// fetched. That also keeps them from pointing this machine at its local network.
func refusalReason(parsed *url.URL) string {
	if !siteHosts[strings.ToLower(parsed.Hostname())] {
		return "hosted on another site, so it is not fetched automatically"
	}
	// robots.txt disallows /bildearkiv, but the site's owner has given permission.
	path := parsed.EscapedPath()
	if !strings.HasPrefix(path, "/i/uploads/") && !strings.HasPrefix(path, "/bildearkiv/") {
		return "not in the forum's image folders"
	}
	return ""
}

// webpToRGB converts lossy WebP to RGB. Its colors use the limited video range, but Go's
// YCbCr type assumes the full range of JPEG, which would turn white into light grey.
func webpToRGB(decoded image.Image) image.Image {
	var source *image.YCbCr
	switch picture := decoded.(type) {
	case *image.YCbCr:
		source = picture
	case *image.NYCbCrA:
		source = &picture.YCbCr
	default:
		return decoded // Lossless WebP is already RGB.
	}
	clamp := func(value float64) uint8 {
		return uint8(max(0, min(255, value+0.5)))
	}
	bounds := source.Bounds()
	output := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			luma := 1.164 * (float64(source.Y[source.YOffset(x, y)]) - 16)
			cb := float64(source.Cb[source.COffset(x, y)]) - 128
			cr := float64(source.Cr[source.COffset(x, y)]) - 128
			output.SetRGBA(x, y, color.RGBA{
				R: clamp(luma + 1.596*cr),
				G: clamp(luma - 0.813*cr - 0.391*cb),
				B: clamp(luma + 2.018*cb),
				A: 255,
			})
		}
	}
	return output
}

// dropAlpha makes an image opaque, keeping the colors of its transparent pixels. JPEG has no
// transparency, and without this the resize would darken them.
func dropAlpha(decoded image.Image) image.Image {
	if opaque, ok := decoded.(interface{ Opaque() bool }); ok && opaque.Opaque() {
		return decoded
	}
	bounds := decoded.Bounds()
	output := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := color.NRGBAModel.Convert(decoded.At(x, y)).(color.NRGBA)
			pixel.A = 255
			output.SetNRGBA(x, y, pixel)
		}
	}
	return output
}

// toJPEG converts an image to a JPEG small enough to send to Claude.
func toJPEG(data []byte) ([]byte, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, &UnavailableError{Reason: fmt.Sprintf("could not be read as an image: %v", err)}
	}
	if !allowedFormats[format] {
		return nil, &UnavailableError{Reason: "could not be read as an image: unsupported format " + format}
	}
	if config.Width*config.Height > maxPixels {
		return nil, &UnavailableError{Reason: "is unreasonably large"}
	}
	decoded, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, &UnavailableError{Reason: fmt.Sprintf("could not be read as an image: %v", err)}
	}
	if format == "webp" {
		decoded = webpToRGB(decoded)
	}
	var output bytes.Buffer
	resized := imaging.Fit(dropAlpha(decoded), maxSidePixels, maxSidePixels, imaging.CatmullRom)
	if err := imaging.Encode(&output, resized, imaging.JPEG, imaging.JPEGQuality(jpegQuality)); err != nil {
		return nil, &UnavailableError{Reason: fmt.Sprintf("could not be read as an image: %v", err)}
	}
	return output.Bytes(), nil
}

// CachePath is where the image cache lives, kept apart from the archive so the crawl can't
// block it.
func CachePath() string {
	return filepath.Join(filepath.Dir(db.Path), "images.db")
}

// ConnectCache opens the image cache, creating it if needed.
func ConnectCache() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(CachePath()), 0o755); err != nil {
		return nil, err
	}
	conn, err := db.Open(CachePath(), "")
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(`
        CREATE TABLE IF NOT EXISTS images (
            url TEXT PRIMARY KEY,
            jpeg BLOB NOT NULL,
            fetched_at TEXT NOT NULL
        )
        `); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func download(client *http.Client, parsed *url.URL) ([]byte, error) {
	secure := *parsed
	secure.Scheme = "https"
	request, err := http.NewRequest(http.MethodGet, secure.String(), nil)
	if err != nil {
		return nil, &UnavailableError{Reason: fmt.Sprintf("download failed: %v", err)}
	}
	request.Header.Set("User-Agent", crawl.UserAgent)
	request.Header.Set("Accept", "*/*")
	response, err := client.Do(request)
	if err != nil {
		return nil, &UnavailableError{Reason: fmt.Sprintf("download failed: %v", err)}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &UnavailableError{Reason: fmt.Sprintf("HTTP %d", response.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, &UnavailableError{Reason: fmt.Sprintf("download failed: %v", err)}
	}
	if len(data) > maxDownloadBytes {
		return nil, &UnavailableError{Reason: "is over the download size limit"}
	}
	return data, nil
}

// GetJPEG returns the image from the cache, downloading it the first time. Problems with the
// image itself are an UnavailableError.
func GetJPEG(cache *sql.DB, client *http.Client, imageURL string) ([]byte, error) {
	parsed, err := url.Parse(imageURL)
	if err != nil {
		return nil, &UnavailableError{Reason: "hosted on another site, so it is not fetched automatically"}
	}
	if reason := refusalReason(parsed); reason != "" {
		return nil, &UnavailableError{Reason: reason}
	}
	var cached []byte
	err = cache.QueryRow("SELECT jpeg FROM images WHERE url = ?", imageURL).Scan(&cached)
	if err == nil {
		return cached, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	data, err := download(client, parsed)
	if err != nil {
		return nil, err
	}
	jpeg, err := toJPEG(data)
	if err != nil {
		return nil, err
	}
	if _, err := cache.Exec(
		"INSERT OR REPLACE INTO images (url, jpeg, fetched_at) VALUES (?, ?, ?)",
		imageURL, jpeg, time.Now().UTC().Format("2006-01-02T15:04:05+00:00"),
	); err != nil {
		return nil, err
	}
	return jpeg, nil
}

// NewClient returns an HTTP client that doesn't follow redirects, so a download can't leave
// the forum's site.
func NewClient() *http.Client {
	return &http.Client{
		Timeout: downloadTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
