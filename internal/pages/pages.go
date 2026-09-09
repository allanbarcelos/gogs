// Package pages serves repositories' published static sites (Gogs Pages) on a
// dedicated domain, so user-authored HTML never runs on the Gogs application
// origin.
package pages

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/gogs/git-module"
	log "unknwon.dev/clog/v2"

	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/database"
	"gogs.io/gogs/internal/gitx"
	"gogs.io/gogs/internal/pathx"
)

// maxPagesBlobBytes is the largest published blob that will be served. A
// public endpoint that buffers or streams without a cap can pin the process
// on a single GET of a multi-gigabyte object.
var maxPagesBlobBytes int64 = 32 << 20

var errSiteNotFound = errors.New("pages site not found")

type site struct {
	gitPath string
	branch  string
	dir     string
	enabled bool
	repoID  int64
}

type store interface {
	lookup(ctx context.Context, owner, repo string) (*site, error)
}

type dbStore struct{}

func (dbStore) lookup(ctx context.Context, ownerName, repoName string) (*site, error) {
	owner, err := database.Handle.Users().GetByUsername(ctx, ownerName)
	if err != nil {
		if database.IsErrUserNotExist(err) {
			return nil, errSiteNotFound
		}
		return nil, errors.Wrap(err, "get owner")
	}

	repo, err := database.Handle.Repositories().GetByName(ctx, owner.ID, repoName)
	if err != nil {
		if database.IsErrRepoNotExist(err) {
			return nil, errSiteNotFound
		}
		return nil, errors.Wrap(err, "get repository")
	}

	page, err := database.Handle.Pages().Get(ctx, repo.ID)
	if err != nil {
		if database.IsErrRepoPageNotFound(err) {
			return nil, errSiteNotFound
		}
		return nil, errors.Wrap(err, "get pages configuration")
	}

	return &site{
		gitPath: repo.RepoPath(),
		branch:  page.Branch,
		dir:     page.Dir,
		enabled: page.Enabled,
		repoID:  repo.ID,
	}, nil
}

// Handler returns an http.Handler that serves published sites for requests on
// the Gogs Pages domain and delegates every other request to next. State-changing
// application requests whose Origin or Sec-Fetch-Site is not the application
// origin are rejected so published JavaScript on a sibling subdomain cannot
// CSRF a logged-in visitor.
func Handler(next http.Handler) http.Handler {
	return newHandler(next, dbStore{})
}

func newHandler(next http.Handler, sites store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, repoName, filePath, ok := parsePagesRequest(stripPort(r.Host), r.URL.Path)
		if !ok {
			if !allowStateChange(r) {
				httpError(w, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		serve(w, r, sites, owner, repoName, filePath)
	})
}

func stripPort(host string) string {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}

// isDNSLabel reports whether s is a single DNS label, i.e., it can be used as
// the left-most label of "<owner>.<PAGES_DOMAIN>".
func isDNSLabel(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

// parsePagesRequest reports whether the request should be served as Gogs Pages
// and, if so, the owner, repository, and file path. Owners that are a single
// DNS label are addressed as "<owner>.<PAGES_DOMAIN>/<repo>/". Owners that are
// not (e.g., they contain "." or "_") are addressed as
// "<PAGES_DOMAIN>/<owner>/<repo>/".
func parsePagesRequest(host, urlPath string) (owner, repo, file string, ok bool) {
	host = strings.ToLower(host)
	domain := conf.Server.PagesDomain
	if domain == "" {
		return "", "", "", false
	}

	if host == domain {
		// The Pages apex is never the application host (rejected at startup), so
		// every request here is Pages traffic even when the path is incomplete.
		rest := strings.TrimPrefix(urlPath, "/")
		owner, rest, _ := strings.Cut(rest, "/")
		owner = strings.ToLower(owner)
		repo, file, _ := strings.Cut(rest, "/")
		if isDNSLabel(owner) {
			return "", "", "", true
		}
		return owner, repo, file, true
	}

	owner, ok = ownerFromHost(host)
	if !ok {
		return "", "", "", false
	}
	repo, file, _ = strings.Cut(strings.TrimPrefix(urlPath, "/"), "/")
	return owner, repo, file, true
}

// ownerFromHost returns the repository owner encoded as the left-most label of a
// Pages host, e.g., "alice" for "alice.pages.example.com" when PAGES_DOMAIN is
// "pages.example.com". The second return value is false when the request is not
// for the Pages domain and should be handled by the main application.
func ownerFromHost(host string) (string, bool) {
	host = strings.ToLower(host)
	domain := conf.Server.PagesDomain
	if domain == "" || host == domain {
		return "", false
	}

	label := strings.TrimSuffix(host, "."+domain)
	if label == host || label == "" || !isDNSLabel(label) {
		return "", false
	}
	return label, true
}

// pagesPublicAuthority is the host (and port, when EXTERNAL_URL has one) used
// in published site URLs. Pages is served on the same listener as the app.
func pagesPublicAuthority(label string) string {
	host := conf.Server.PagesDomain
	if label != "" {
		host = label + "." + host
	}
	if u := conf.Server.URL; u != nil {
		if port := u.Port(); port != "" {
			return host + ":" + port
		}
	}
	return host
}

// SiteURL returns the public URL of a repository's published site.
func SiteURL(owner, repo string) string {
	owner = strings.ToLower(owner)
	scheme := conf.Server.PagesURLScheme()
	if isDNSLabel(owner) {
		return scheme + "://" + pagesPublicAuthority(owner) + "/" + repo + "/"
	}
	return scheme + "://" + pagesPublicAuthority("") + "/" + url.PathEscape(owner) + "/" + repo + "/"
}

func applicationHost() string {
	if conf.Server.URL == nil {
		return ""
	}
	return strings.ToLower(conf.Server.URL.Host)
}

func originHost(origin string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" || strings.EqualFold(origin, "null") {
		return ""
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}

func originMatchesRequest(origin string, r *http.Request) bool {
	host := originHost(origin)
	if host == "" {
		return false
	}
	if host == strings.ToLower(r.Host) {
		return true
	}
	app := applicationHost()
	return app != "" && host == app
}

// allowStateChange reports whether a non-Pages request may proceed. GET, HEAD,
// and OPTIONS are always allowed. Other methods are allowed when Origin is
// missing (git HTTP, curl) or when Origin's host is this application. Origin
// is a forbidden header, so a match is first-party even if Sec-Fetch-Site is
// wrong (Safari reports cross-site for some localhost form POSTs). Requests
// whose Origin is another host, including a Pages site, are rejected.
func allowStateChange(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}

	// Commit status reports authenticate with HMAC or a write token, not a
	// session cookie. CI hosts send their own Origin; rejecting them would
	// block the documented Jenkins path when Pages is enabled.
	if isCommitStatusReport(r) {
		return true
	}

	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if originMatchesRequest(origin, r) {
		return true
	}

	// Gogs pages set Referrer-Policy: no-referrer, and some browsers then send
	// Origin: null on first-party form POSTs. Trust Sec-Fetch-Site in that case.
	site := strings.ToLower(r.Header.Get("Sec-Fetch-Site"))
	if strings.EqualFold(origin, "null") && (site == "" || site == "same-origin" || site == "none") {
		return true
	}

	log.Trace("Pages: rejected %s %s origin=%q host=%q site=%q", r.Method, r.URL.Path, origin, r.Host, r.Header.Get("Sec-Fetch-Site"))
	return false
}

// isCommitStatusReport reports whether r is POST /api/v1/repos/:owner/:repo/statuses/:sha
// (with an optional application subpath). Other API writes are not exempted:
// an invalid token header must not skip the Origin check, because auth then
// falls through to the session cookie.
func isCommitStatusReport(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	path := r.URL.Path
	if sub := strings.TrimSuffix(conf.Server.Subpath, "/"); sub != "" && sub != "/" {
		path = strings.TrimPrefix(path, sub)
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 7 {
		return false
	}
	return parts[0] == "api" && parts[1] == "v1" && parts[2] == "repos" && parts[5] == "statuses" &&
		parts[3] != "" && parts[4] != "" && parts[6] != ""
}

func serve(w http.ResponseWriter, r *http.Request, sites store, ownerName, repoName, filePath string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpError(w, http.StatusMethodNotAllowed)
		return
	}
	if ownerName == "" || repoName == "" {
		httpError(w, http.StatusNotFound)
		return
	}

	ctx := r.Context()

	page, err := sites.lookup(ctx, ownerName, repoName)
	if err != nil {
		if errors.Is(err, errSiteNotFound) {
			httpError(w, http.StatusNotFound)
			return
		}
		log.Error("Pages: failed to look up %q/%q: %v", ownerName, repoName, err)
		httpError(w, http.StatusInternalServerError)
		return
	}
	if !page.enabled {
		httpError(w, http.StatusNotFound)
		return
	}

	gitRepo, err := git.Open(page.gitPath)
	if err != nil {
		log.Error("Pages: failed to open repository %d: %v", page.repoID, err)
		httpError(w, http.StatusInternalServerError)
		return
	}

	commit, err := gitRepo.BranchCommit(page.branch)
	if err != nil {
		if gitx.IsErrRevisionNotExist(err) {
			httpError(w, http.StatusNotFound)
			return
		}
		log.Error("Pages: failed to resolve branch %q of repository %d: %v", page.branch, page.repoID, err)
		httpError(w, http.StatusInternalServerError)
		return
	}

	baseDir := pathx.Clean(page.dir)
	treePath := path.Join(baseDir, pathx.Clean(filePath))

	needsSlash := !strings.HasSuffix(r.URL.Path, "/")
	if treePath == "" {
		if needsSlash {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		treePath = "index.html"
	}

	entry, err := commit.TreeEntry(treePath)
	if err == nil && entry.IsTree() {
		if needsSlash {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		treePath = path.Join(treePath, "index.html")
		entry, err = commit.TreeEntry(treePath)
	}
	if err != nil || !isServableBlob(entry) {
		serveNotFound(w, r, commit, baseDir)
		return
	}

	serveBlob(w, r, entry, commit.ID.String()+":"+treePath)
}

func isServableBlob(entry *git.TreeEntry) bool {
	return entry != nil && (entry.IsBlob() || entry.IsExec() || entry.IsSymlink())
}

func serveBlob(w http.ResponseWriter, r *http.Request, entry *git.TreeEntry, etagBody string) {
	if entry.Size() > maxPagesBlobBytes {
		httpError(w, http.StatusRequestEntityTooLarge)
		return
	}

	etag := `"` + etagBody + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	name := entry.Name()
	ctype := mime.TypeByExtension(filepath.Ext(name))
	blob := entry.Blob()

	if r.Method == http.MethodHead {
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", strconv.FormatInt(entry.Size(), 10))
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusOK)
		return
	}

	if ctype == "" {
		data, err := blob.Bytes()
		if err != nil {
			log.Error("Pages: failed to read blob %q: %v", name, err)
			httpError(w, http.StatusInternalServerError)
			return
		}
		setContentType(w, name, data)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}

	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(entry.Size(), 10))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.WriteHeader(http.StatusOK)
	if err := blob.Pipeline(w, io.Discard); err != nil {
		log.Error("Pages: failed to stream blob %q: %v", name, err)
	}
}

// serveNotFound serves a "404.html" from the site's directory when present,
// otherwise a plain not-found response.
func serveNotFound(w http.ResponseWriter, r *http.Request, commit *git.Commit, baseDir string) {
	entry, err := commit.TreeEntry(path.Join(baseDir, "404.html"))
	if err != nil || !isServableBlob(entry) || entry.Size() > maxPagesBlobBytes {
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
