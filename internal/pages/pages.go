// Package pages serves repositories' published static sites (Gogs Pages) on a
// dedicated domain, so user-authored HTML never runs on the Gogs application
// origin.
package pages

import (
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gogs/git-module"
	log "unknwon.dev/clog/v2"

	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/database"
	"gogs.io/gogs/internal/gitx"
	"gogs.io/gogs/internal/pathx"
)

// Handler returns an http.Handler that serves published sites for requests on
// the Gogs Pages domain and delegates every other request to next.
func Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, ok := ownerFromHost(stripPort(r.Host))
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		serve(w, r, owner)
	})
}

func stripPort(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}

// ownerFromHost returns the repository owner encoded as the left-most label of a
// Pages host, e.g. "alice" for "alice.pages.example.com" when PAGES_DOMAIN is
// "pages.example.com". The second return value is false when the request is not
// for the Pages domain and should be handled by the main application.
func ownerFromHost(host string) (string, bool) {
	host = strings.ToLower(host)
	domain := conf.Server.PagesDomain
	if domain == "" || host == domain {
		return "", false
	}

	label := strings.TrimSuffix(host, "."+domain)
	if label == host || label == "" || strings.Contains(label, ".") {
		return "", false
	}
	return label, true
}

func serve(w http.ResponseWriter, r *http.Request, ownerName string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpError(w, http.StatusMethodNotAllowed)
		return
	}

	repoName, filePath, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if repoName == "" {
		httpError(w, http.StatusNotFound)
		return
	}

	ctx := r.Context()

	owner, err := database.Handle.Users().GetByUsername(ctx, ownerName)
	if err != nil {
		if !database.IsErrUserNotExist(err) {
			log.Error("Pages: failed to get owner %q: %v", ownerName, err)
			httpError(w, http.StatusInternalServerError)
			return
		}
		httpError(w, http.StatusNotFound)
		return
	}

	repo, err := database.Handle.Repositories().GetByName(ctx, owner.ID, repoName)
	if err != nil {
		if !database.IsErrRepoNotExist(err) {
			log.Error("Pages: failed to get repository %q/%q: %v", ownerName, repoName, err)
			httpError(w, http.StatusInternalServerError)
			return
		}
		httpError(w, http.StatusNotFound)
		return
	}

	// 🚨 SECURITY: An enabled site is always public, even when the repository is
	// private. This mirrors GitHub Pages and is surfaced to the admin in the
	// repository settings.
	page, err := database.Handle.Pages().Get(ctx, repo.ID)
	if err != nil {
		if !database.IsErrRepoPageNotFound(err) {
			log.Error("Pages: failed to get configuration for repository %d: %v", repo.ID, err)
			httpError(w, http.StatusInternalServerError)
			return
		}
		httpError(w, http.StatusNotFound)
		return
	}
	if !page.Enabled {
		httpError(w, http.StatusNotFound)
		return
	}

	gitRepo, err := git.Open(repo.RepoPath())
	if err != nil {
		log.Error("Pages: failed to open repository %d: %v", repo.ID, err)
		httpError(w, http.StatusInternalServerError)
		return
	}

	commit, err := gitRepo.CatFileCommit(page.Branch)
	if err != nil {
		if gitx.IsErrRevisionNotExist(err) {
			httpError(w, http.StatusNotFound)
			return
		}
		log.Error("Pages: failed to resolve branch %q of repository %d: %v", page.Branch, repo.ID, err)
		httpError(w, http.StatusInternalServerError)
		return
	}

	// 🚨 SECURITY: pathx.Clean removes any traversal ("..", leading slashes,
	// backslashes) so the resolved tree path can never escape the configured
	// directory.
	baseDir := pathx.Clean(page.Dir)
	treePath := path.Join(baseDir, pathx.Clean(filePath))
	if treePath == "" {
		treePath = "index.html"
	}

	entry, err := commit.TreeEntry(treePath)
	if err == nil && entry.IsTree() {
		// Redirect "/repo/dir" to "/repo/dir/" so relative links inside the
		// served "index.html" resolve against the directory, matching GitHub Pages.
		if filePath != "" && !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		treePath = path.Join(treePath, "index.html")
		entry, err = commit.TreeEntry(treePath)
	}
	if err != nil || entry.IsTree() {
		serveNotFound(w, r, commit, baseDir)
		return
	}

	data, err := entry.Blob().Bytes()
	if err != nil {
		log.Error("Pages: failed to read %q of repository %d: %v", treePath, repo.ID, err)
		httpError(w, http.StatusInternalServerError)
		return
	}

	etag := `"` + commit.ID.String() + ":" + treePath + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	setContentType(w, treePath, data)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

// serveNotFound serves a "404.html" from the site's directory when present,
// otherwise a plain not-found response.
func serveNotFound(w http.ResponseWriter, r *http.Request, commit *git.Commit, baseDir string) {
	entry, err := commit.TreeEntry(path.Join(baseDir, "404.html"))
	if err != nil || entry.IsTree() {
		httpError(w, http.StatusNotFound)
		return
	}
	data, err := entry.Blob().Bytes()
	if err != nil {
		httpError(w, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusNotFound)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func setContentType(w http.ResponseWriter, name string, data []byte) {
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		ctype = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ctype)
}

func httpError(w http.ResponseWriter, status int) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.Error(w, http.StatusText(status), status)
}
