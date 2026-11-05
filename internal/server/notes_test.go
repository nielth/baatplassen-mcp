package server

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func photo(t *testing.T) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 40, 20))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, picture); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

// noRedirects lets the tests see where the page sends the browser.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func postNote(t *testing.T, server *httptest.Server, text string, photos ...[]byte) (*http.Response, string) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("text", text); err != nil {
		t.Fatal(err)
	}
	for _, data := range photos {
		part, err := form.CreateFormFile("photos", "bilde.png")
		if err != nil {
			t.Fatal(err)
		}
		part.Write(data)
	}
	form.Close()
	response, err := noRedirects.Post(server.URL+"/"+secret+"/notes", form.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	page, _ := io.ReadAll(response.Body)
	return response, string(page)
}

func get(t *testing.T, server *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	response, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response, string(body)
}

func TestNoteSavedByClaudeIsFoundAgain(t *testing.T) {
	server := testServer(t)
	result := callTool(t, server, "save_note", map[string]any{"text": "  Byttet impeller, Johnson 09-1027B  "})
	saved := result["structuredContent"].(map[string]any)
	if result["isError"] == true || saved["note_id"] != float64(1) || saved["text"] != "Byttet impeller, Johnson 09-1027B" {
		t.Fatalf("result = %v", result)
	}
	result = callTool(t, server, "search_notes", map[string]any{"query": "IMPELLER"})
	blocks := texts(result)
	if len(blocks) != 1 || !strings.Contains(blocks[0], "09-1027B") || !strings.Contains(blocks[0], `"created_at"`) {
		t.Errorf("blocks = %v", blocks)
	}
	if blocks := texts(callTool(t, server, "search_notes", map[string]any{"query": "seil"})); len(blocks) != 0 {
		t.Errorf("unexpected match: %v", blocks)
	}
}

func TestEmptyNoteIsRefused(t *testing.T) {
	result := callTool(t, testServer(t), "save_note", map[string]any{"text": "   "})
	if result["isError"] != true {
		t.Errorf("result = %v", result)
	}
}

func TestDeletedNoteIsGone(t *testing.T) {
	server := testServer(t)
	callTool(t, server, "save_note", map[string]any{"text": "Feil motor"})
	if got := texts(callTool(t, server, "delete_note", map[string]any{"note_id": 1})); got[0] != "Deleted note 1" {
		t.Errorf("got %v", got)
	}
	if got := texts(callTool(t, server, "delete_note", map[string]any{"note_id": 1})); got[0] != "No note with id 1" {
		t.Errorf("got %v", got)
	}
	if got := texts(callTool(t, server, "search_notes", map[string]any{})); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func TestPhotoUploadedOnThePageReachesClaude(t *testing.T) {
	server := testServer(t)
	response, _ := postNote(t, server, "Typeskilt på motoren", photo(t), photo(t))
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "notes" {
		t.Fatalf("status %d, location %q", response.StatusCode, response.Header.Get("Location"))
	}
	result := callTool(t, server, "get_note_images", map[string]any{"note_id": 1})
	content := result["content"].([]any)
	if len(content) != 3 || content[0].(map[string]any)["text"] != "Note 1 has 2 photo(s), showing 2." {
		t.Fatalf("content = %v", result)
	}
	if block := content[1].(map[string]any); block["type"] != "image" || block["mimeType"] != "image/jpeg" {
		t.Errorf("block = %v", block)
	}
	for _, start := range []int{5, -1} {
		if result := callTool(t, server, "get_note_images", map[string]any{"note_id": 1, "start": start}); result["isError"] == true {
			t.Errorf("start %d: %v", start, result)
		}
	}
	if got := texts(callTool(t, server, "get_note_images", map[string]any{"note_id": 7})); got[0] != "No note with id 7" {
		t.Errorf("got %v", got)
	}
}

func TestPageListsNotesWithPhotos(t *testing.T) {
	server := testServer(t)
	postNote(t, server, "Motor <Yanmar>", photo(t))
	response, page := get(t, server, "/"+secret+"/notes")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
	if !strings.Contains(page, "Motor &lt;Yanmar&gt;") || !strings.Contains(page, `src="notes/images/1"`) {
		t.Errorf("page = %s", page)
	}
	if response.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers = %v", response.Header)
	}
	response, body := get(t, server, "/"+secret+"/notes/images/1")
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "image/jpeg" || !strings.HasPrefix(body, "\xff\xd8") {
		t.Errorf("image: status %d, type %s", response.StatusCode, response.Header.Get("Content-Type"))
	}
	for _, path := range []string{"/notes/images/1", "/" + secret + "/notes/images/2", "/" + secret + "/notes/images/x"} {
		if response, _ := get(t, server, path); response.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d", path, response.StatusCode)
		}
	}
}

func TestBadUploadsKeepTheTextAndSaveNothing(t *testing.T) {
	server := testServer(t)
	for _, c := range []struct {
		text   string
		photos [][]byte
		want   string
	}{
		{"", nil, "Skriv noe eller legg til et bilde."},
		{"Ikke et bilde", [][]byte{[]byte("<html>")}, "bilde.png kunne ikke brukes"},
		{"For mange", bytes.SplitAfter(bytes.Repeat([]byte("x"), 11), []byte("x"))[:11], "Høyst 10 bilder"},
	} {
		response, page := postNote(t, server, c.text, c.photos...)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(page, c.want) || !strings.Contains(page, c.text) {
			t.Errorf("%q: status %d, page %s", c.text, response.StatusCode, page)
		}
	}
	if got := texts(callTool(t, server, "search_notes", map[string]any{})); len(got) != 0 {
		t.Errorf("saved anyway: %v", got)
	}
}

func TestNoteWithOnlyAPhotoIsSaved(t *testing.T) {
	server := testServer(t)
	if response, _ := postNote(t, server, "", photo(t)); response.StatusCode != http.StatusSeeOther {
		t.Errorf("status %d", response.StatusCode)
	}
}
