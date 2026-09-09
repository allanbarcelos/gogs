package pages

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gogs.io/gogs/internal/conf"
)

func TestStripPort(t *testing.T) {
	assert.Equal(t, "alice.pages.example.com", stripPort("alice.pages.example.com"))
	assert.Equal(t, "alice.pages.example.com", stripPort("alice.pages.example.com:3000"))
	assert.Equal(t, "", stripPort(""))
}

func TestIsDNSLabel(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{in: "alice", want: true},
		{in: "foo-bar", want: true},
		{in: "a", want: true},
		{in: "foo.bar", want: false},
		{in: "foo_bar", want: false},
		{in: "-alice", want: false},
		{in: "alice-", want: false},
		{in: "", want: false},
		{in: strings.Repeat("a", 64), want: false},
	}
	for _, test := range tests {
		t.Run(test.in, func(t *testing.T) {
			assert.Equal(t, test.want, isDNSLabel(test.in))
		})
	}
}

func TestOwnerFromHost(t *testing.T) {
	t.Run("feature disabled", func(t *testing.T) {
		conf.Server.PagesDomain = ""
		_, ok := ownerFromHost("alice.pages.example.com")
		assert.False(t, ok)
	})

	conf.Server.PagesDomain = "pages.example.com"
	t.Cleanup(func() { conf.Server.PagesDomain = "" })

	t.Run("application host", func(t *testing.T) {
		_, ok := ownerFromHost("gogs.example.com")
		assert.False(t, ok)
	})

	t.Run("apex pages domain", func(t *testing.T) {
		_, ok := ownerFromHost("pages.example.com")
		assert.False(t, ok)
	})

	t.Run("nested label is not a valid owner", func(t *testing.T) {
		_, ok := ownerFromHost("a.b.pages.example.com")
		assert.False(t, ok)
	})

	t.Run("underscore is not a DNS label", func(t *testing.T) {
		_, ok := ownerFromHost("foo_bar.pages.example.com")
		assert.False(t, ok)
	})

	t.Run("valid owner", func(t *testing.T) {
		owner, ok := ownerFromHost("alice.pages.example.com")
		assert.True(t, ok)
		assert.Equal(t, "alice", owner)
	})

	t.Run("owner label is lower-cased", func(t *testing.T) {
		owner, ok := ownerFromHost("Alice.pages.example.com")
		assert.True(t, ok)
		assert.Equal(t, "alice", owner)
	})
}

func TestSiteURL(t *testing.T) {
	setPagesConf(t)

	assert.Equal(t, "https://alice.pages.example.com/repo/", SiteURL("Alice", "repo"))
	assert.Equal(t, "https://pages.example.com/foo.bar/repo/", SiteURL("foo.bar", "repo"))
	assert.Equal(t, "https://pages.example.com/foo_bar/repo/", SiteURL("foo_bar", "repo"))

	u, err := url.Parse("http://localhost:3000/")
	require.NoError(t, err)
	conf.Server.URL = u
	conf.Server.PagesProtocol = "http"
	assert.Equal(t, "http://alice.pages.example.com:3000/repo/", SiteURL("alice", "repo"))
	assert.Equal(t, "http://pages.example.com:3000/foo.bar/repo/", SiteURL("foo.bar", "repo"))
}

func TestParsePagesRequest(t *testing.T) {
	setPagesConf(t)

	t.Run("subdomain owner", func(t *testing.T) {
		owner, repo, file, ok := parsePagesRequest("alice.pages.example.com", "/repo/style.css")
		assert.True(t, ok)
		assert.Equal(t, "alice", owner)
		assert.Equal(t, "repo", repo)
		assert.Equal(t, "style.css", file)
	})

	t.Run("apex dotted owner", func(t *testing.T) {
		owner, repo, file, ok := parsePagesRequest("pages.example.com", "/foo.bar/repo/index.html")
		assert.True(t, ok)
		assert.Equal(t, "foo.bar", owner)
		assert.Equal(t, "repo", repo)
		assert.Equal(t, "index.html", file)
	})

	t.Run("apex DNS-label owner is still pages traffic", func(t *testing.T) {
		owner, repo, _, ok := parsePagesRequest("pages.example.com", "/alice/repo/")
		assert.True(t, ok)
		assert.Equal(t, "", owner)
		assert.Equal(t, "", repo)
	})

	t.Run("application host", func(t *testing.T) {
		_, _, _, ok := parsePagesRequest("gogs.example.com", "/alice/repo/")
		assert.False(t, ok)
	})
}

func TestAllowStateChange(t *testing.T) {
	setPagesConf(t)

	get := httptest.NewRequest(http.MethodGet, "https://gogs.example.com/user/settings", nil)
	get.Header.Set("Origin", "https://alice.pages.example.com")
	assert.True(t, allowStateChange(get))

	postApp := httptest.NewRequest(http.MethodPost, "https://gogs.example.com/user/settings", nil)
	postApp.Host = "gogs.example.com"
	postApp.Header.Set("Origin", "https://gogs.example.com")
	postApp.Header.Set("Sec-Fetch-Site", "same-origin")
	assert.True(t, allowStateChange(postApp))

	postFormNone := httptest.NewRequest(http.MethodPost, "http://localhost:3000/repo/create", nil)
	postFormNone.Host = "localhost:3000"
	postFormNone.Header.Set("Origin", "http://localhost:3000")
	postFormNone.Header.Set("Sec-Fetch-Site", "none")
	assert.True(t, allowStateChange(postFormNone))

	postSafariLocalhost := httptest.NewRequest(http.MethodPost, "http://localhost:3000/repo/create", nil)
	postSafariLocalhost.Host = "localhost:3000"
	postSafariLocalhost.Header.Set("Origin", "http://localhost:3000")
	postSafariLocalhost.Header.Set("Sec-Fetch-Site", "cross-site")
	assert.True(t, allowStateChange(postSafariLocalhost))

	postTLSTerm := httptest.NewRequest(http.MethodPost, "http://gogs.localhost/repo/create", nil)
	postTLSTerm.Host = "gogs.localhost"
	postTLSTerm.Header.Set("Origin", "https://gogs.localhost")
	assert.True(t, allowStateChange(postTLSTerm))

	postLoopback := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:3000/repo/create", nil)
	postLoopback.Host = "127.0.0.1:3000"
	postLoopback.Header.Set("Origin", "http://127.0.0.1:3000")
	postLoopback.Header.Set("Sec-Fetch-Site", "same-origin")
	assert.True(t, allowStateChange(postLoopback))

	postPages := httptest.NewRequest(http.MethodPost, "https://gogs.example.com/user/settings", nil)
	postPages.Host = "gogs.example.com"
	postPages.Header.Set("Origin", "https://alice.pages.example.com")
	postPages.Header.Set("Sec-Fetch-Site", "same-site")
	assert.False(t, allowStateChange(postPages))

	postGit := httptest.NewRequest(http.MethodPost, "https://gogs.example.com/alice/repo.git/git-receive-pack", nil)
	assert.True(t, allowStateChange(postGit))

	postCI := httptest.NewRequest(http.MethodPost, "https://gogs.example.com/api/v1/repos/alice/repo/statuses/deadbeef", nil)
	postCI.Host = "gogs.example.com"
	postCI.Header.Set("Origin", "https://jenkins.example.com")
	assert.True(t, allowStateChange(postCI))

	postCIOtherAPI := httptest.NewRequest(http.MethodPost, "https://gogs.example.com/api/v1/user/emails", nil)
	postCIOtherAPI.Host = "gogs.example.com"
	postCIOtherAPI.Header.Set("Origin", "https://jenkins.example.com")
	postCIOtherAPI.Header.Set("Authorization", "token dummy")
	assert.False(t, allowStateChange(postCIOtherAPI))

	postNullOrigin := httptest.NewRequest(http.MethodPost, "http://localhost:3000/repo/create", nil)
	postNullOrigin.Host = "localhost:3000"
	postNullOrigin.Header.Set("Origin", "null")
	postNullOrigin.Header.Set("Sec-Fetch-Site", "same-origin")
	assert.True(t, allowStateChange(postNullOrigin))

	postNullCrossSite := httptest.NewRequest(http.MethodPost, "http://localhost:3000/repo/create", nil)
	postNullCrossSite.Host = "localhost:3000"
	postNullCrossSite.Header.Set("Origin", "null")
	postNullCrossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	assert.False(t, allowStateChange(postNullCrossSite))
}

func TestHandlerOriginGuard(t *testing.T) {
	setPagesConf(t)
	h := newHandler(appHandler(), mapStore{})

	t.Run("pages origin is rejected on application POST", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://gogs.example.com/user/settings", nil)
		req.Host = "gogs.example.com"
		req.Header.Set("Origin", "https://alice.pages.example.com")
		req.Header.Set("Sec-Fetch-Site", "same-site")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("application origin is allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "https://gogs.example.com/user/settings", nil)
		req.Host = "gogs.example.com"
		req.Header.Set("Origin", "https://gogs.example.com")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusTeapot, rec.Code)
	})

	t.Run("form POST with Sec-Fetch-Site none is allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://localhost:3000/repo/create", nil)
		req.Host = "localhost:3000"
		req.Header.Set("Origin", "http://localhost:3000")
		req.Header.Set("Sec-Fetch-Site", "none")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusTeapot, rec.Code)
	})
}

func TestServe(t *testing.T) {
	setPagesConf(t)

	repoPath, branch := initPagesRepo(t, map[string]string{
		"index.html":      "<h1>home</h1>\n<link rel=\"stylesheet\" href=\"style.css\">",
		"style.css":       "body{color:black}",
		"docs/index.html": "<h1>docs</h1>",
		"404.html":        "<h1>missing</h1>",
		"dir/index.html":  "<h1>dir</h1>",
		"big.bin":         strings.Repeat("x", 32),
	})
	linkPath := filepath.Join(repoPath, "link.html")
	if err := os.Symlink("index.html", linkPath); err != nil {
		t.Logf("symlink not created: %v", err)
	} else {
		gitRun(t, repoPath, "add", "link.html")
		gitRun(t, repoPath, "commit", "-m", "symlink")
	}

	sites := mapStore{
		"alice/site": {
			gitPath: repoPath,
			branch:  branch,
			dir:     "/",
			enabled: true,
			repoID:  1,
		},
		"alice/docs": {
			gitPath: repoPath,
			branch:  branch,
			dir:     "/docs",
			enabled: true,
			repoID:  1,
		},
		"alice/off": {
			gitPath: repoPath,
			branch:  branch,
			dir:     "/",
			enabled: false,
			repoID:  1,
		},
		"foo.bar/site": {
			gitPath: repoPath,
			branch:  branch,
			dir:     "/",
			enabled: true,
			repoID:  1,
		},
	}
	h := newHandler(appHandler(), sites)

	t.Run("index at repo root redirects to trailing slash", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/site")
		assert.Equal(t, http.StatusMovedPermanently, rec.Code)
		assert.Equal(t, "/site/", rec.Header().Get("Location"))
	})

	t.Run("index at repo root", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/site/")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "<h1>home</h1>")
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	})

	t.Run("subdirectory without slash redirects", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/site/dir")
		assert.Equal(t, http.StatusMovedPermanently, rec.Code)
		assert.Equal(t, "/site/dir/", rec.Header().Get("Location"))
	})

	t.Run("css asset", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/site/style.css")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "body{color:black}", rec.Body.String())
		assert.Contains(t, rec.Header().Get("Content-Type"), "text/css")
	})

	t.Run("docs directory", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/docs/")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "<h1>docs</h1>")
	})

	t.Run("custom 404", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/site/nope.html")
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), "<h1>missing</h1>")
	})

	t.Run("disabled site", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/off/")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("unknown owner", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "nobody.pages.example.com", "/site/")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("HEAD", func(t *testing.T) {
		rec := doPages(t, h, http.MethodHead, "alice.pages.example.com", "/site/style.css")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Body.String())
	})

	t.Run("method not allowed", func(t *testing.T) {
		rec := doPages(t, h, http.MethodPost, "alice.pages.example.com", "/site/")
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("path traversal stays in the tree", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/site/../style.css")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "body{color:black}", rec.Body.String())
	})

	t.Run("symlink is served as the raw target", func(t *testing.T) {
		if _, err := os.Lstat(linkPath); err != nil {
			t.Skip("symlink not available")
		}
		rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/site/link.html")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "index.html", rec.Body.String())
	})

	t.Run("dotted owner on apex", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "pages.example.com", "/foo.bar/site/")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "<h1>home</h1>")
	})

	t.Run("DNS-label owner on apex is not served", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "pages.example.com", "/alice/site/")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("application host is not captured", func(t *testing.T) {
		rec := doPages(t, h, http.MethodGet, "gogs.example.com", "/alice/site/")
		assert.Equal(t, http.StatusTeapot, rec.Code)
	})

	t.Run("blob larger than the cap", func(t *testing.T) {
		orig := maxPagesBlobBytes
		maxPagesBlobBytes = 8
		t.Cleanup(func() { maxPagesBlobBytes = orig })

		rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/site/big.bin")
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})
}

func TestServeBranchNotTag(t *testing.T) {
	setPagesConf(t)

	repoPath, branch := initPagesRepo(t, map[string]string{
		"index.html": "<h1>branch</h1>",
	})
	gitRun(t, repoPath, "tag", "pages")

	sites := mapStore{
		"alice/site": {
			gitPath: repoPath,
			branch:  "pages",
			dir:     "/",
			enabled: true,
			repoID:  1,
		},
	}
	h := newHandler(appHandler(), sites)

	rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/site/")
	assert.Equal(t, http.StatusNotFound, rec.Code)

	sites["alice/site"].branch = branch
	rec = doPages(t, h, http.MethodGet, "alice.pages.example.com", "/site/")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "<h1>branch</h1>")
}

func TestServeLookupError(t *testing.T) {
	setPagesConf(t)
	h := newHandler(appHandler(), errStore{err: errors.New("db down")})

	rec := doPages(t, h, http.MethodGet, "alice.pages.example.com", "/site/")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

type mapStore map[string]*site

func (m mapStore) lookup(_ context.Context, owner, repo string) (*site, error) {
	s, ok := m[owner+"/"+repo]
	if !ok {
		return nil, errSiteNotFound
	}
	return s, nil
}

type errStore struct{ err error }

func (s errStore) lookup(context.Context, string, string) (*site, error) {
	return nil, s.err
}

func appHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "app")
	})
}

func setPagesConf(t *testing.T) {
	t.Helper()
	origDomain := conf.Server.PagesDomain
	origURL := conf.Server.URL
	origProto := conf.Server.PagesProtocol
	u, err := url.Parse("https://gogs.example.com/")
	require.NoError(t, err)
	conf.Server.PagesDomain = "pages.example.com"
	conf.Server.PagesProtocol = "https"
	conf.Server.URL = u
	t.Cleanup(func() {
		conf.Server.PagesDomain = origDomain
		conf.Server.URL = origURL
		conf.Server.PagesProtocol = origProto
	})
}

func doPages(t *testing.T, h http.Handler, method, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func initPagesRepo(t *testing.T, files map[string]string) (repoPath, branch string) {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "pages@example.com")
	gitRun(t, dir, "config", "user.name", "Pages Test")
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "init")

	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	require.NoError(t, err, string(out))
	return dir, strings.TrimSpace(string(out))
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Pages Test",
		"GIT_AUTHOR_EMAIL=pages@example.com",
		"GIT_COMMITTER_NAME=Pages Test",
		"GIT_COMMITTER_EMAIL=pages@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+dir,
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
}
