package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// staticAliases maps doc routes that would collide with the REST/MCP paths
// to their actual files under the website root. /mcp and /api are live API
// endpoints, so their docs pages are reachable at these aliases instead.
var staticAliases = map[string]string{
	"/mcp-tools": "/mcp.html",       // MCP docs (website/mcp.html)
	"/docs":      "/api/index.html", // API docs (website/api/index.html)
}

// staticSite serves the marketing/docs site from a directory root. It is
// reached only for paths the router did not match to a REST/MCP/healthz
// route, and it requires no key.
type staticSite struct {
	root string
}

func newStaticSite(root string) *staticSite {
	return &staticSite{root: root}
}

func (s *staticSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	rel := s.resolvePath(r.URL.Path)
	full := filepath.Join(s.root, filepath.FromSlash(rel))
	if !s.withinRoot(full) {
		s.serve404(w, r)
		return
	}

	if info, err := os.Stat(full); err != nil || info.IsDir() {
		s.serve404(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

// resolvePath maps a request path to a relative path under the website
// root. Docs pages live at /<name>.html and also answer at /<name> without
// the extension; directories resolve to their index.html.
func (s *staticSite) resolvePath(p string) string {
	if alias, ok := staticAliases[p]; ok {
		return strings.TrimPrefix(alias, "/")
	}
	clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(p)), "/")
	if clean == "" || clean == "." {
		return "index.html"
	}
	if filepath.Ext(clean) == "" {
		if s.fileExists(clean + ".html") {
			return clean + ".html"
		}
		return clean + "/index.html"
	}
	return clean
}

// fileExists reports whether rel names a regular file under the website root.
func (s *staticSite) fileExists(rel string) bool {
	full := filepath.Join(s.root, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	return err == nil && !info.IsDir()
}

// withinRoot reports whether path resolves inside the website root, guarding
// against traversal outside the served directory.
func (s *staticSite) withinRoot(full string) bool {
	root, err := filepath.Abs(s.root)
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(full)
	if err != nil {
		return false
	}
	return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
}

// serve404 falls back to <root>/404.html when present, otherwise a plain 404.
func (s *staticSite) serve404(w http.ResponseWriter, r *http.Request) {
	fallback := filepath.Join(s.root, "404.html")
	if s.withinRoot(fallback) {
		if info, err := os.Stat(fallback); err == nil && !info.IsDir() {
			w.WriteHeader(http.StatusNotFound)
			http.ServeFile(w, r, fallback)
			return
		}
	}
	http.NotFound(w, r)
}
