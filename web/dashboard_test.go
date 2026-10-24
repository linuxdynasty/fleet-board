package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// contentFS is a dashboard-shaped tree: the entry page, one hashed asset,
// and one unhashed file, so the table can tell exact hits from fallbacks.
func contentFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html><title>fleet</title>")},
		"assets/app-ab12cd34.js": {Data: []byte("console.log(1)")},
		"favicon.ico":            {Data: []byte("icon")},
	}
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return method(t, h, http.MethodGet, path)
}

func method(t *testing.T, h http.Handler, httpMethod, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), httpMethod, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandler(t *testing.T) {
	t.Parallel()
	h := Handler(contentFS())
	cases := []struct {
		name       string
		path       string
		wantBody   string
		wantType   string
		wantCache  string
		wantStatus int
	}{
		{name: "root serves the entry page", path: "/", wantBody: "<!doctype html><title>fleet</title>", wantType: "text/html", wantCache: "no-cache", wantStatus: http.StatusOK},
		{name: "exact entry page is not cached", path: "/index.html", wantBody: "<!doctype html><title>fleet</title>", wantType: "text/html", wantCache: "no-cache", wantStatus: http.StatusOK},
		{name: "hashed asset is immutable", path: "/assets/app-ab12cd34.js", wantBody: "console.log(1)", wantType: "text/javascript", wantCache: "public, max-age=31536000, immutable", wantStatus: http.StatusOK},
		{name: "unhashed file has no cache orders", path: "/favicon.ico", wantBody: "icon", wantType: "image/x-icon", wantCache: "", wantStatus: http.StatusOK},
		{name: "unknown view path falls back to the entry page", path: "/usage", wantBody: "<!doctype html><title>fleet</title>", wantType: "text/html", wantCache: "no-cache", wantStatus: http.StatusOK},
		{name: "unknown nested path falls back to the entry page", path: "/session/abc/windows", wantBody: "<!doctype html><title>fleet</title>", wantType: "text/html", wantCache: "no-cache", wantStatus: http.StatusOK},
		{name: "dot-dot stays inside the tree", path: "/assets/../usage", wantBody: "<!doctype html><title>fleet</title>", wantType: "text/html", wantCache: "no-cache", wantStatus: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := get(t, h, tc.path)
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s status = %d, want %d", tc.path, rec.Code, tc.wantStatus)
			}
			body, err := io.ReadAll(rec.Result().Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != tc.wantBody {
				t.Fatalf("GET %s body = %q, want %q", tc.path, body, tc.wantBody)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.wantType) {
				t.Fatalf("GET %s Content-Type = %q, want prefix %q", tc.path, got, tc.wantType)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.wantCache {
				t.Fatalf("GET %s Cache-Control = %q, want %q", tc.path, got, tc.wantCache)
			}
		})
	}
}

func TestHandlerRefusesWrites(t *testing.T) {
	t.Parallel()
	h := Handler(contentFS())
	if rec := method(t, h, http.MethodPost, "/usage"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /usage status = %d, want 405", rec.Code)
	}
}

func TestDistHoldsThePlaceholder(t *testing.T) {
	t.Parallel()
	content, err := Dist()
	if err != nil {
		t.Fatal(err)
	}
	f, err := content.Open("index.html")
	if err != nil {
		t.Fatalf("embedded dashboard has no index.html: %v", err)
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 {
		t.Fatal("embedded index.html is empty; go build needs the placeholder")
	}
}
