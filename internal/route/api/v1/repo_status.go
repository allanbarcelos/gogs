package v1

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/cockroachdb/errors"
	"github.com/gogs/git-module"
	"gopkg.in/macaron.v1"
	log "unknwon.dev/clog/v2"

	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/context"
	"gogs.io/gogs/internal/database"
	"gogs.io/gogs/internal/gitx"
	"gogs.io/gogs/internal/route/api/v1/types"
)

const (
	commitStatusRawBodyKey   = "commitStatusRawBody"
	commitStatusViaSecretKey = "commitStatusViaSecret"

	// maxCommitStatusBodySize bounds the request body of a status report. A
	// well-formed payload is well under 1 KiB.
	maxCommitStatusBodySize = 16 << 10
	// maxCommitStatusContextLen matches the indexed column width.
	maxCommitStatusContextLen = 191
	// maxCommitStatusDescriptionLen keeps descriptions to a single line.
	maxCommitStatusDescriptionLen = 1000
	// maxCommitStatusTargetURLLen is a generous cap for a build console URL.
	maxCommitStatusTargetURLLen = 2000
)

// commitStatusAssignment loads the repository for a commit status report and
// authenticates the request in one of two ways:
//
//   - X-Gogs-Signature: an HMAC-SHA256 of the raw body keyed by the
//     repository's CI secret. No user, no token.
//   - Authorization: token <PAT>, requiring write access, for callers that
//     prefer a user identity.
//
// The raw body is buffered so both the signature check and the handler can
// read it.
func commitStatusAssignment() macaron.Handler {
	return func(c *context.APIContext) {
		username := c.Params(":username")
		reponame := strings.TrimSuffix(c.Params(":reponame"), ".git")

		owner, err := database.Handle.Users().GetByUsername(c.Req.Context(), username)
		if err != nil {
			c.NotFoundOrError(err, "get user by name")
			return
		}
		repo, err := database.Handle.Repositories().GetByName(c.Req.Context(), owner.ID, reponame)
		if err != nil {
			c.NotFoundOrError(err, "get repository by name")
			return
		}
		if err = repo.GetOwner(); err != nil {
			c.Error(err, "get owner")
			return
		}
		c.Repo.Owner = owner
		c.Repo.Repository = repo

		body, err := io.ReadAll(io.LimitReader(c.Req.Request.Body, maxCommitStatusBodySize+1))
		if err != nil {
			c.Error(err, "read request body")
			return
		}
		if len(body) > maxCommitStatusBodySize {
			c.ErrorStatus(http.StatusRequestEntityTooLarge, errors.New("Request body is too large."))
			return
		}
		c.Data[commitStatusRawBodyKey] = body

		signature := c.Req.Header.Get("X-Gogs-Signature")
		in := commitStatusAuthInput{
			Private:   repo.IsPrivate,
			Signature: signature,
			Secret:    repo.CommitStatusSecret,
			Body:      body,
			TokenAuth: c.IsTokenAuth,
		}
		if signature == "" && c.IsTokenAuth {
			c.Repo.AccessMode = database.Handle.Permissions().AccessMode(c.Req.Context(), c.UserID(), repo.ID,
				database.AccessModeOptions{OwnerID: repo.OwnerID, Private: repo.IsPrivate},
			)
			in.HasAccess = c.Repo.HasAccess()
			in.IsWriter = c.Repo.IsWriter()
		}

		switch decideCommitStatusAuth(in) {
		case commitStatusAuthViaSecret:
			c.Data[commitStatusViaSecretKey] = true
			return
		case commitStatusAuthViaToken:
			return
		case commitStatusAuthNotFound:
			c.NotFound()
			return
		case commitStatusAuthForbidden:
			c.Status(http.StatusForbidden)
			return
		case commitStatusAuthUnauthorized:
			c.ErrorStatus(http.StatusUnauthorized, errors.New("Provide either X-Gogs-Signature or an access token."))
			return
		case commitStatusAuthBadSignature:
			c.ErrorStatus(http.StatusUnauthorized, errors.New("Invalid signature."))
			return
		default:
			c.NotFound()
			return
		}
	}
}

type commitStatusAuthInput struct {
	Private   bool
	Signature string
	Secret    string
	Body      []byte
	TokenAuth bool
	HasAccess bool
	IsWriter  bool
}

type commitStatusAuthOutcome int

const (
	commitStatusAuthViaSecret commitStatusAuthOutcome = iota
	commitStatusAuthViaToken
	commitStatusAuthNotFound
	commitStatusAuthForbidden
	commitStatusAuthUnauthorized
	commitStatusAuthBadSignature
)

// decideCommitStatusAuth chooses how a status report is authenticated. A
// private repository that the caller cannot prove access to is indistinguishable
// from a missing one (404).
func decideCommitStatusAuth(in commitStatusAuthInput) commitStatusAuthOutcome {
	if in.Signature != "" {
		if !database.VerifyCommitStatusSignature(in.Secret, in.Body, in.Signature) {
			if in.Private {
				return commitStatusAuthNotFound
			}
			return commitStatusAuthBadSignature
		}
		return commitStatusAuthViaSecret
	}
	if !in.TokenAuth {
		if in.Private {
			return commitStatusAuthNotFound
		}
		return commitStatusAuthUnauthorized
	}
	if !in.HasAccess {
		return commitStatusAuthNotFound
	}
	if !in.IsWriter {
		return commitStatusAuthForbidden
	}
	return commitStatusAuthViaToken
}

// parseCommitStatusState maps a wire state string to a database state. It
// accepts the GitHub set (pending, success, failure, error) plus the "running"
// extension. The second return value is false for an unrecognized value.
func parseCommitStatusState(s string) (database.CommitStatusState, bool) {
	state := database.CommitStatusState(s)
	if !state.IsValid() {
		return "", false
	}
	return state, true
}

// resolveCommitID turns a ref (full SHA, short SHA, branch, or tag) into the
// canonical 40-character commit SHA for the repository in context.
func resolveCommitID(c *context.APIContext, ref string) (string, error) {
	gitRepo, err := git.Open(c.Repo.Repository.RepoPath())
	if err != nil {
		return "", errors.Wrap(err, "open repository")
	}
	return gitRepo.RevParse(ref)
}

func toCommitStatus(status *database.CommitStatus, creator *database.User) *types.CommitStatus {
	s := &types.CommitStatus{
		ID:          status.ID,
		State:       string(status.State),
		TargetURL:   status.TargetURL,
		Description: status.Description,
		Context:     status.Context,
		CreatorName: status.CreatorName,
		Created:     status.Created,
		Updated:     status.Updated,
	}
	if creator != nil {
		s.Creator = toUser(creator)
	}
	return s
}

// mustEnableCommitStatus renders 404 when the repository has the builds feature
// turned off, or when the instance has disabled commit statuses.
func mustEnableCommitStatus(c *context.APIContext) {
	if !c.Repo.Repository.ShowsCommitStatus() {
		c.NotFound()
		return
	}
}

// ciCreatorName labels statuses reported with the repository CI secret.
const ciCreatorName = "ci"

// isAcceptableTargetURL reports whether a status target URL is safe to store
// and later render as a link. An empty value is allowed; anything else must be
// an absolute http or https URL within the length cap. This rejects
// "javascript:" and other script-bearing schemes.
func isAcceptableTargetURL(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > maxCommitStatusTargetURLLen {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return true
	}
	return false
}

// createCommitStatus reports a CI check outcome for a commit. It runs behind
// commitStatusAssignment, which has loaded the repository, buffered the raw
// body, and authenticated the request.
//
// POST /repos/:username/:reponame/statuses/:sha
func createCommitStatus(c *context.APIContext) {
	raw, _ := c.Data[commitStatusRawBodyKey].([]byte)
	var form types.CreateStatusOption
	if err := json.Unmarshal(raw, &form); err != nil {
		c.ErrorStatus(http.StatusBadRequest, errors.New("Malformed JSON body."))
		return
	}

	state, ok := parseCommitStatusState(form.State)
	if !ok {
		c.ErrorStatus(http.StatusUnprocessableEntity, errors.New("Invalid state, must be one of: pending, running, success, failure, error."))
		return
	}

	form.Context = strings.TrimSpace(form.Context)
	if utf8.RuneCountInString(form.Context) > maxCommitStatusContextLen {
		c.ErrorStatus(http.StatusUnprocessableEntity, errors.New("Context is too long."))
		return
	}
	if utf8.RuneCountInString(form.Description) > maxCommitStatusDescriptionLen {
		c.ErrorStatus(http.StatusUnprocessableEntity, errors.New("Description is too long."))
		return
	}
	if !isAcceptableTargetURL(form.TargetURL) {
		c.ErrorStatus(http.StatusUnprocessableEntity, errors.New("target_url must be an absolute http or https URL."))
		return
	}

	commitID, err := resolveCommitID(c, c.Params(":sha"))
	if err != nil {
		c.NotFoundOrError(gitx.NewError(err), "resolve commit ID")
		return
	}

	viaSecret, _ := c.Data[commitStatusViaSecretKey].(bool)
	opts := database.CreateCommitStatusOptions{
		RepoID:      c.Repo.Repository.ID,
		CommitSHA:   commitID,
		State:       state,
		Context:     form.Context,
		TargetURL:   form.TargetURL,
		Description: form.Description,
		MaxContexts: conf.Repository.CommitStatus.MaxContextsPerCommit,
	}
	if viaSecret {
		opts.CreatorName = ciCreatorName
	} else {
		opts.CreatorID = c.User.ID
	}

	status, err := database.Handle.CommitStatuses().Create(c.Req.Context(), opts)
	if err != nil {
		if database.IsErrTooManyCommitStatusContexts(err) {
			c.ErrorStatus(http.StatusUnprocessableEntity, errors.New("Too many distinct contexts on this commit."))
			return
		}
		c.Error(err, "create commit status")
		return
	}

	payload := &types.WebhookStatusPayload{
		SHA:         commitID,
		State:       string(status.State),
		Context:     status.Context,
		Description: status.Description,
		TargetURL:   status.TargetURL,
		Repository:  c.Repo.Repository.APIFormatLegacy(nil),
	}
	if !viaSecret {
		payload.Sender = c.User.APIFormat()
	}
	if err := database.PrepareWebhooks(c.Repo.Repository, database.HookEventTypeStatus, payload); err != nil {
		log.Error("Failed to prepare webhooks for %q: %v", database.HookEventTypeStatus, err)
	}

	var creator *database.User
	if !viaSecret {
		creator = c.User
	}
	c.JSON(http.StatusCreated, toCommitStatus(status, creator))
}

// listCommitStatuses returns every status for a ref, all contexts, newest
// first.
//
// GET /repos/:username/:reponame/commits/:sha/statuses
func listCommitStatuses(c *context.APIContext) {
	commitID, err := resolveCommitID(c, c.Params(":sha"))
	if err != nil {
		c.NotFoundOrError(gitx.NewError(err), "resolve commit ID")
		return
	}

	statuses, err := database.Handle.CommitStatuses().List(c.Req.Context(), c.Repo.Repository.ID, commitID)
	if err != nil {
		c.Error(err, "list commit statuses")
		return
	}

	c.JSONSuccess(commitStatusesToAPI(c, statuses))
}

// getCombinedCommitStatus returns the combined status for a ref plus the latest
// status per context.
//
// GET /repos/:username/:reponame/commits/:sha/status
func getCombinedCommitStatus(c *context.APIContext) {
	commitID, err := resolveCommitID(c, c.Params(":sha"))
	if err != nil {
		c.NotFoundOrError(gitx.NewError(err), "resolve commit ID")
		return
	}

	latest, err := database.Handle.CommitStatuses().Latest(c.Req.Context(), c.Repo.Repository.ID, commitID)
	if err != nil {
		c.Error(err, "list latest commit statuses")
		return
	}

	apiStatuses := commitStatusesToAPI(c, latest)
	states := make([]database.CommitStatusState, len(latest))
	for i, st := range latest {
		states[i] = st.State
	}

	c.JSONSuccess(&types.CombinedStatus{
		State:      string(database.CombineCommitStatusStates(states...)),
		SHA:        commitID,
		TotalCount: len(apiStatuses),
		Statuses:   apiStatuses,
	})
}

// commitStatusesToAPI converts a list of statuses, resolving each distinct
// creator once.
func commitStatusesToAPI(c *context.APIContext, statuses []*database.CommitStatus) []*types.CommitStatus {
	creators := make(map[int64]*database.User)
	result := make([]*types.CommitStatus, 0, len(statuses))
	for _, status := range statuses {
		var creator *database.User
		if status.CreatorID != 0 {
			var ok bool
			creator, ok = creators[status.CreatorID]
			if !ok {
				if u, err := database.Handle.Users().GetByID(c.Req.Context(), status.CreatorID); err == nil {
					creator = u
				}
				creators[status.CreatorID] = creator
			}
		}
		result = append(result, toCommitStatus(status, creator))
	}
	return result
}
