package images

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nielth/baatplassen-mcp/internal/db"
)

const (
	upload      = "https://baatplassen.no/i/uploads/monthly_2026_10/a.jpg"
	whiteWebP   = "UklGRiYAAABXRUJQVlA4IBoAAAAwAQCdASoQABAAAMASJaQAA3AA/v7deAAAAA=="
	rotatedJPEG = "/9j/4AAQSkZJRgABAQAAAQABAAD/4QAiRXhpZgAATU0AKgAAAAgAAQESAAMAAAABAAYAAAAAAAD/2wBDAAIBAQEBAQIBAQECAgICAgQDAgICAgUEBAMEBgUGBgYFBgYGBwkIBgcJBwYGCAsICQoKCgoKBggLDAsKDAkKCgr/2wBDAQICAgICAgUDAwUKBwYHCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgoKCgr/wAARCAAIABADASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL/8QAtRAAAgEDAwIEAwUFBAQAAAF9AQIDAAQRBRIhMUEGE1FhByJxFDKBkaEII0KxwRVS0fAkM2JyggkKFhcYGRolJicoKSo0NTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqDhIWGh4iJipKTlJWWl5iZmqKjpKWmp6ipqrKztLW2t7i5usLDxMXGx8jJytLT1NXW19jZ2uHi4+Tl5ufo6erx8vP09fb3+Pn6/8QAHwEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoL/8QAtREAAgECBAQDBAcFBAQAAQJ3AAECAxEEBSExBhJBUQdhcRMiMoEIFEKRobHBCSMzUvAVYnLRChYkNOEl8RcYGRomJygpKjU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6goOEhYaHiImKkpOUlZaXmJmaoqOkpaanqKmqsrO0tba3uLm6wsPExcbHyMnK0tPU1dbX2Nna4uPk5ebn6Onq8vP09fb3+Pn6/9oADAMBAAIRAxEAPwD5Y/4Jr+IP2XUk8GW37XcWoJ4TTwTAqyWRmMf2sWsfl+eLcef5WN+PK+bzPK3fJvr9S9e/4I4+MPjvpmg/Gj9lTxT4U03wZ4p8OWeqaZa+IdWvllKTJ5kciD7NIyo8TRSbZGLhnYELgKCivsPE/wAD+DcingeIsBOvCVemoTo+1boXspuooSTkqjbs3z8ttFFbnl8SZ5mXilhoZfxJJVo4b+FLlUZQWicYuKSSkox5vdvLljzN8sbf/9k="
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func pngBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, picture); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func clientReturning(status int, content []byte, calls *[]string) *http.Client {
	client := NewClient()
	client.Transport = roundTrip(func(request *http.Request) (*http.Response, error) {
		*calls = append(*calls, request.URL.String())
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(bytes.NewReader(content)),
			Header:     http.Header{},
			Request:    request,
		}, nil
	})
	return client
}

func tempCache(t *testing.T) *sql.DB {
	t.Helper()
	db.Path = filepath.Join(t.TempDir(), "archive.db")
	cache, err := ConnectCache()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cache.Close() })
	return cache
}

func TestOnlyTheForumsUploadsAreFetched(t *testing.T) {
	for _, url := range []string{
		"https://baatplassen.no/i/theme_resources/a.png",
		"https://example.com/i/uploads/a.jpg",
		"http://192.168.1.1/i/uploads/a.jpg",
		"https://baatplassen.no.evil.example/i/uploads/a.jpg",
		"https://baatplassen.no/i/%75ploads/a.jpg",
		"://not a url",
	} {
		var calls []string
		_, err := GetJPEG(tempCache(t), clientReturning(200, pngBytes(t, 10, 10), &calls), url)
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Errorf("%s: err = %v", url, err)
		}
		if len(calls) != 0 {
			t.Errorf("%s: fetched %v", url, calls)
		}
	}
}

func TestOldImageArchiveIsFetched(t *testing.T) {
	var calls []string
	_, err := GetJPEG(tempCache(t), clientReturning(200, pngBytes(t, 10, 10), &calls), "https://baatplassen.no/bildearkiv/43/a.jpg")
	if err != nil || len(calls) != 1 {
		t.Errorf("err = %v, calls = %v", err, calls)
	}
}

func TestPlainHTTPUploadIsFetchedOverHTTPS(t *testing.T) {
	var calls []string
	_, err := GetJPEG(tempCache(t), clientReturning(200, pngBytes(t, 10, 10), &calls), "http://www.baatplassen.no/i/uploads/a.jpg")
	if err != nil || len(calls) != 1 || calls[0] != "https://www.baatplassen.no/i/uploads/a.jpg" {
		t.Errorf("err = %v, calls = %v", err, calls)
	}
}

func TestImageIsResizedAndCached(t *testing.T) {
	var calls []string
	cache := tempCache(t)
	client := clientReturning(200, pngBytes(t, 3000, 1500), &calls)
	first, err := GetJPEG(cache, client, upload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GetJPEG(cache, client, upload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || len(calls) != 1 {
		t.Errorf("not served from the cache: %d calls", len(calls))
	}
	decoded, err := jpeg.Decode(bytes.NewReader(first))
	if err != nil {
		t.Fatal(err)
	}
	if size := decoded.Bounds().Size(); size.X != 1568 || size.Y != 784 {
		t.Errorf("size = %v", size)
	}
}

func TestSmallImageIsNotEnlarged(t *testing.T) {
	converted, err := toJPEG(pngBytes(t, 100, 50))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(converted))
	if err != nil {
		t.Fatal(err)
	}
	if size := decoded.Bounds().Size(); size.X != 100 || size.Y != 50 {
		t.Errorf("size = %v", size)
	}
}

func TestFailuresAreReportedAndNotCached(t *testing.T) {
	for _, c := range []struct {
		status  int
		content []byte
	}{{404, nil}, {302, nil}, {200, []byte("<html>not an image")}} {
		var calls []string
		cache := tempCache(t)
		_, err := GetJPEG(cache, clientReturning(c.status, c.content, &calls), upload)
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) {
			t.Errorf("%d: err = %v", c.status, err)
		}
		var count int
		if err := cache.QueryRow("SELECT COUNT(*) FROM images").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("%d: cached %d images", c.status, count)
		}
	}
}

func TestOversizedDownloadIsRefused(t *testing.T) {
	var calls []string
	content := bytes.Repeat([]byte("x"), maxDownloadBytes+1)
	_, err := GetJPEG(tempCache(t), clientReturning(200, content, &calls), upload)
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Errorf("err = %v", err)
	}
}

func TestDecompressionBombIsRefused(t *testing.T) {
	// Small as a file, but huge once decoded: it must be refused before decoding.
	var output bytes.Buffer
	picture := image.NewPaletted(image.Rect(0, 0, 20000, 20000), color.Palette{color.Black, color.White})
	if err := png.Encode(&output, picture); err != nil {
		t.Fatal(err)
	}
	_, err := toJPEG(output.Bytes())
	if err == nil || !strings.Contains(err.Error(), "unreasonably large") {
		t.Errorf("err = %v", err)
	}
}

func decodedJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()
	converted, err := toJPEG(data)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(converted))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func fromBase64(t *testing.T, text string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPhotoIsTurnedTheWayItsCameraSays(t *testing.T) {
	// 16x8 JPEG with EXIF orientation 6, as phones write when held upright.
	size := decodedJPEG(t, fromBase64(t, rotatedJPEG)).Bounds().Size()
	if size.X != 8 || size.Y != 16 {
		t.Errorf("size = %v", size)
	}
}

func TestLossyWebPKeepsItsWhites(t *testing.T) {
	// Go reads WebP's limited color range as full range unless converted, giving grey.
	r, g, b, _ := decodedJPEG(t, fromBase64(t, whiteWebP)).At(8, 8).RGBA()
	if r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Errorf("white came out as %d %d %d", r>>8, g>>8, b>>8)
	}
}

func TestTransparentPixelsKeepTheirColor(t *testing.T) {
	picture := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for y := range 10 {
		for x := range 10 {
			picture.SetNRGBA(x, y, color.NRGBA{R: 200, G: 200, B: 200, A: 0})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, picture); err != nil {
		t.Fatal(err)
	}
	r, _, _, _ := decodedJPEG(t, output.Bytes()).At(5, 5).RGBA()
	if value := r >> 8; value < 190 || value > 210 {
		t.Errorf("red = %d, want about 200", value)
	}
}

func TestOtherFormatsAreRefused(t *testing.T) {
	// Registered by the imaging library, but not something forum posts should bring in.
	tiffHeader := []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	var unavailable *UnavailableError
	if _, err := toJPEG(tiffHeader); !errors.As(err, &unavailable) {
		t.Errorf("err = %v", err)
	}
}

func TestImageURLs(t *testing.T) {
	got := URLs("Se her [bilde: " + upload + "] og [bilde: ]")
	if !reflect.DeepEqual(got, []string{upload}) {
		t.Errorf("urls = %v", got)
	}
}
