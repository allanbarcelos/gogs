package v1

import (
	"net/http"

	"github.com/cockroachdb/errors"
	"github.com/gogs/git-module"
	log "unknwon.dev/clog/v2"

	"gogs.io/gogs/internal/context"
	"gogs.io/gogs/internal/database"
	"gogs.io/gogs/internal/gitx"
	"gogs.io/gogs/internal/route/api/v1/types"
)

// defaultMaxCommitStatusContexts caps the number of distinct contexts a single
// commit may carry. Stage 4 replaces this with a configurable value.
const defaultMaxCommitStatusContexts = 20

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
		Created:     status.Created,
		Updated:     status.Updated,
	}
	if creator != nil {
		s.Creator = toUser(creator)
	}
	return s
}

// mustEnableCommitStatus renders 404 when the repository has the builds feature
// turned off.
func mustEnableCommitStatus(c *context.APIContext) {
	if !c.Repo.Repository.EnableCommitStatus {
		c.NotFound()
	}
}

// createCommitStatus reports a CI check outcome for a commit.
//
// POST /repos/:username/:reponame/statuses/:sha
func createCommitStatus(c *context.APIContext, form types.CreateStatusOption) {
	state, ok := parseCommitStatusState(form.State)
	if !ok {
		c.ErrorStatus(http.StatusUnprocessableEntity, errors.New("Invalid state, must be one of: pending, running, success, failure, error."))
		return
	}

	commitID, err := resolveCommitID(c, c.Params(":sha"))
	if err != nil {
		c.NotFoundOrError(gitx.NewError(err), "resolve commit ID")
		return
	}

	status, err := database.Handle.CommitStatuses().Create(c.Req.Context(), database.CreateCommitStatusOptions{
		RepoID:      c.Repo.Repository.ID,
		CreatorID:   c.User.ID,
		CommitSHA:   commitID,
		State:       state,
		Context:     form.Context,
		TargetURL:   form.TargetURL,
		Description: form.Description,
		MaxContexts: defaultMaxCommitStatusContexts,
	})
	if err != nil {
		if database.IsErrTooManyCommitStatusContexts(err) {
			c.ErrorStatus(http.StatusUnprocessableEntity, errors.New("Too many distinct contexts on this commit."))
			return
		}
		c.Error(err, "create commit status")
		return
	}

	if err := database.PrepareWebhooks(c.Repo.Repository, database.HookEventTypeStatus, &types.WebhookStatusPayload{
		SHA:         commitID,
		State:       string(status.State),
		Context:     status.Context,
		Description: status.Description,
		TargetURL:   status.TargetURL,
		Repository:  c.Repo.Repository.APIFormatLegacy(nil),
		Sender:      c.User.APIFormat(),
	}); err != nil {
		log.Error("Failed to prepare webhooks for %q: %v", database.HookEventTypeStatus, err)
	}

	c.JSON(http.StatusCreated, toCommitStatus(status, c.User))
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
		creator, ok := creators[status.CreatorID]
		if !ok {
			u, err := database.Handle.Users().GetByID(c.Req.Context(), status.CreatorID)
			if err == nil {
				creator = u
			}
			creators[status.CreatorID] = creator
		}
		result = append(result, toCommitStatus(status, creator))
	}
	return result
}
