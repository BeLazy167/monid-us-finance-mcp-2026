package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func newTestSite(t *testing.T) *staticSite {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"index.html":        "<html>home</html>",
		"pricing.html":      "<html>pricing</html>",
		"mcp.html":          "<html>mcp docs</html>",
		"api/index.html":    "<html>api docs</html>",
		"subdir/index.html": "<html>subdir</html>",
		"404.html":          "<html>not found</html>",
	}
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return newStaticSite(root)
}

func serveStatic(t *testing.T, s *staticSite, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestStaticSite_IndexAndExtensionlessRoutes(t *testing.T) {
	s := newTestSite(t)

	rec := serveStatic(t, s, "/")
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>home</html>" {
		t.Fatalf("/ = %d %q", rec.Code, rec.Body.String())
	}

	rec = serveStatic(t, s, "/pricing")
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>pricing</html>" {
		t.Fatalf("/pricing = %d %q", rec.Code, rec.Body.String())
	}

	rec = serveStatic(t, s, "/subdir/")
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>subdir</html>" {
		t.Fatalf("/subdir/ = %d %q", rec.Code, rec.Body.String())
	}
}

func TestStaticSite_AliasesForMCPAndAPIDocs(t *testing.T) {
	s := newTestSite(t)

	rec := serveStatic(t, s, "/mcp-tools")
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>mcp docs</html>" {
		t.Fatalf("/mcp-tools = %d %q", rec.Code, rec.Body.String())
	}

	rec = serveStatic(t, s, "/docs")
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>api docs</html>" {
		t.Fatalf("/docs = %d %q", rec.Code, rec.Body.String())
	}
}

func TestStaticSite_404FallbackAndTraversalGuard(t *testing.T) {
	s := newTestSite(t)

	rec := serveStatic(t, s, "/nope")
	if rec.Code != http.StatusNotFound || rec.Body.String() != "<html>not found</html>" {
		t.Fatalf("/nope = %d %q, want 404.html fallback", rec.Code, rec.Body.String())
	}

	rec = serveStatic(t, s, "/../../../etc/passwd")
	if rec.Code == http.StatusOK {
		t.Fatalf("path traversal must not serve a file outside the site root")
	}
}

func TestStaticSite_RejectsNonGetMethods(t *testing.T) {
	s := newTestSite(t)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST / = %d, want 404", rec.Code)
	}
}
