package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// staticAliases maps doc routes that would collide with proxied API prefixes
// to their actual files under the website root. /mcp and /api are live API
// endpoints (proxied), so their docs pages are reachable at these aliases.
var staticAliases = map[string]string{
	"/mcp-tools": "/mcp.html",       // MCP docs (website/mcp.html)
	"/docs":      "/api/index.html", // API docs (website/api/index.html)
}

// handleStatic serves the marketing/docs site from ../website. It is reached
// only for non-API paths (isProxyPath takes precedence) and requires no key.
func (g *gateway) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}

	rel := g.resolveStaticPath(r.URL.Path)
	full := filepath.Join(g.cfg.WebsiteRoot, filepath.FromSlash(rel))
	if !g.withinRoot(full) {
		g.serve404(w, r)
		return
	}

	if info, err := os.Stat(full); err != nil || info.IsDir() {
		g.serve404(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

// resolveStaticPath maps a request path to a relative path under the website
// root. Docs pages live at /<name>.html and also answer at /<name> without the
// extension; directories resolve to their index.html.
func (g *gateway) resolveStaticPath(p string) string {
	if alias, ok := staticAliases[p]; ok {
		return strings.TrimPrefix(alias, "/")
	}
	clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(p)), "/")
	if clean == "" || clean == "." {
		return "index.html"
	}
	if filepath.Ext(clean) == "" {
		if g.fileExists(clean + ".html") {
			return clean + ".html"
		}
		return clean + "/index.html"
	}
	return clean
}

// fileExists reports whether rel names a regular file under the website root.
func (g *gateway) fileExists(rel string) bool {
	full := filepath.Join(g.cfg.WebsiteRoot, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	return err == nil && !info.IsDir()
}

// withinRoot reports whether path resolves inside the website root, guarding
// against traversal outside the served directory.
func (g *gateway) withinRoot(full string) bool {
	root, err := filepath.Abs(g.cfg.WebsiteRoot)
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(full)
	if err != nil {
		return false
	}
	return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
}

// serve404 falls back to website/404.html when present, otherwise a plain 404.
func (g *gateway) serve404(w http.ResponseWriter, r *http.Request) {
	fallback := filepath.Join(g.cfg.WebsiteRoot, "404.html")
	if g.withinRoot(fallback) {
		if info, err := os.Stat(fallback); err == nil && !info.IsDir() {
			w.WriteHeader(http.StatusNotFound)
			http.ServeFile(w, r, fallback)
			return
		}
	}
	http.NotFound(w, r)
}
