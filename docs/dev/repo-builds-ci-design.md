# Design: repository "Builds" tab and commit status API

Status: draft for review. Branch: `feat/repo-actions-ci`.

## Summary

Add a per repository "Builds" tab that shows the CI outcome of each commit and
pull request, fed by an external CI system (Jenkins first). Gogs does not run
builds. It stores build status reported over a new API and renders it.

This is the piece GitHub calls the commit status and checks API, not GitHub
Actions. There is no workflow execution engine, no runners, and no YAML
workflow format in this design.

## Motivation

Today Gogs can trigger a CI system through outbound webhooks (`push` and
`pull_request` events), and the Jenkins `gogs-webhook` plugin already consumes
them. What is missing is the return path: there is no way for the CI system to
report a build result back to Gogs against a commit, and no place in the UI to
see that result. A contributor has to leave Gogs and open Jenkins to know
whether a commit built.

## Terminology

- **Commit status**: one reported outcome for one logical check on one commit,
  e.g., `jenkins/build` on `a1b2c3d` is `success`. This is the internal name
  and the API resource name. It matches the GitHub and Gitea vocabulary so
  existing CI integrations work without adaptation.
- **Build** (UI label only): how the "Builds" tab presents a group of commit
  statuses to the user. The tab, page titles, and locale keys use "Builds".
  The database, Go types, and API routes use "commit status".
- **Combined status**: the aggregate of the most recent status per context for
  a commit. Precedence, worst wins: `error` or `failure` > `running` >
  `pending` > `success`. `running` is a distinct rung above `pending` so the
  aggregate can show "running" separately from "queued". No status at all means
  "no builds".

## Data flow

```
  git push / open pull request
        |
        v
  Gogs sends outbound webhook  ----------->  Jenkins runs the job
  (push / pull_request event,                       |
   already implemented)                             |  on each state transition:
        ^                                           v
        |                  POST /api/v1/repos/:owner/:repo/statuses/:sha
        |                  Authorization: token <PAT of a CI user>
        |                  { state, context, target_url, description }
        |                                           |
        +--------  store commit_status row  <--------+
                          |
                          v
   "Builds" tab  +  status badge in the commit list  +  checks section on the PR
                          |
                          v
   (phase 3, not in this PR) required checks gate the merge button
```

Gogs opens no new outbound connection to Jenkins. The only new network path is
Jenkins calling the Gogs API, authenticated with a personal access token from a
dedicated CI user that has write access to the repository.

## Scope of the first PR

In:

- `commit_status` table and migration.
- Repository flag to enable or disable the feature.
- Three API v1 endpoints: create status, list statuses for a ref, combined
  status for a ref.
- `status` outbound webhook event.
- "Builds" tab in both the React SPA and the legacy Go templates.
- Builds list page and per commit detail page.
- Combined status badge in the commit list and on the repository home header.
- Checks section on the pull request page.
- Instance config section and a retention cron job.
- Developer and user documentation for wiring up Jenkins.

Out, tracked as later work:

- Required status checks on protected branches (phase 3, changes merge rules).
- Re-triggering a build from Gogs.
- Server push updates. The first version polls.
- Status badge image endpoint for READMEs.
- A dedicated Jenkins plugin. The API must be stable enough to make one
  possible, but the plugin is a separate project.

Explicitly never in scope: runners, workflow execution, CI secret management.

## Data model

New table `commit_status`, one file `internal/database/commit_status.go` with a
`CommitStatusesStore` interface, following the store plus generated mock
pattern used across `internal/database`.

| Field         | Column          | Type                                                      | Notes |
|---------------|-----------------|----------------------------------------------------------|-------|
| ID            | id              | BIGSERIAL                                                 | |
| RepoID        | repo_id         | BIGINT NOT NULL                                           | |
| CommitSHA     | commit_sha      | VARCHAR(40) NOT NULL                                      | |
| State         | state           | VARCHAR(20) NOT NULL                                      | `pending`, `running`, `success`, `failure`, `error` |
| Context       | context         | TEXT NOT NULL                                             | logical check id, e.g., `jenkins/build` |
| TargetURL     | target_url      | TEXT                                                      | link to the build console |
| Description   | description     | TEXT                                                      | short line, e.g., `Passed in 3m20s` |
| CreatorID     | creator_id      | BIGINT NOT NULL                                           | user of the token that reported it |
| CreatedUnix   | created_unix    | BIGINT                                                    | |
| UpdatedUnix   | updated_unix    | BIGINT                                                    | |

Indexes:

- `idx_commit_status_repo_commit` on `(repo_id, commit_sha)`.
- `idx_commit_status_repo_commit_context` on `(repo_id, commit_sha, context)`,
  not unique.

Each POST appends a new row. History per context is kept. The "current" status
for a context is the most recent row by `created_unix`. Rendering and the
combined status always reduce to the latest row per context.

`running` is an extension beyond the GitHub set. The create endpoint accepts
the GitHub set (`pending`, `success`, `failure`, `error`) and also `running`.
A caller that only knows GitHub semantics never sends `running` and everything
still works. In the combined status, `running` sits on its own rung between
`pending` and the failing states, so a commit with one context building and the
rest queued aggregates to `running`, not `pending`.

### Repository flag

Add to the `Repository` model in `internal/database/repo.go`, matching the
style of `EnableIssues`, `EnableWiki`, and `EnablePulls`:

```go
EnableCommitStatus bool `xorm:"NOT NULL DEFAULT true" gorm:"not null;default:TRUE"`
```

### Migration

The current `v22` entry in `internal/database/migrations/migrations.go` is a
noop, so the next real migration is `v23`. Add `internal/database/migrations/v23.go`
plus a `NewMigration("create commit_status table", ...)` at the bottom of the
`migrations` slice. The migration runs `AutoMigrate` for the new struct and for
the new `repository.enable_commit_status` column.

Update `docs/dev/database_schema.md` with the new table via the schemadoc
generator.

## API surface

Register inside the existing `m.Group("/repos/:username/:reponame", ...)` block
in `internal/route/api/v1/api.go`. That layer is still on Macaron. New handlers
go in `internal/route/api/v1/repo_status.go`, request and response types in
`internal/route/api/v1/types/status.go`. Payload shapes match GitHub so
existing CI tooling works unchanged.

| Method | Route                          | Auth                              | Purpose |
|--------|--------------------------------|-----------------------------------|---------|
| POST   | `/statuses/:sha`               | `reqToken()` + `reqRepoWriter()`  | create or update a status for a commit |
| GET    | `/commits/:ref/statuses`       | repo read                         | list every status for a ref, all contexts, full history, newest first |
| GET    | `/commits/:ref/status`         | repo read                         | combined status for a ref plus the latest status per context |

`:ref` accepts a full SHA, a short SHA, a branch name, or a tag name, resolved
the same way as other ref accepting endpoints.

POST body:

```json
{
  "state": "success",
  "target_url": "https://jenkins.example.com/job/acme/142/",
  "description": "Passed in 3m20s",
  "context": "jenkins/build"
}
```

`context` defaults to `default` when omitted, matching GitHub. `state` is
required. `MAX_CONTEXTS_PER_COMMIT` is enforced on create: once a commit has
that many distinct contexts, a POST with a new context returns 422.

Every 5xx from these handlers logs the error inside the handler, per the
repository coding guideline.

## Outbound webhook event

Add `HookEventTypeStatus HookEventType = "status"` in
`internal/database/webhook.go`, a `HasStatusEvent` flag in `HookEvents`, a
checkbox in the webhook settings form, and a payload builder. Fired after a
status row is written, so an instance can chain notifications (Slack, Discord,
custom) when a build result changes.

## Permissions

- Write a status: token with write access to the repository, enforced by
  `reqRepoWriter()`. This is the same level as pushing code, so a CI account
  that can already push gains no new capability.
- View statuses and the "Builds" tab: any principal with read access to the
  repository. Visibility follows the repository's public or private state.
- Toggle the feature: repository admin, in repository settings next to
  "Issues" and "Wiki".
- Statuses are append only. There is no edit or delete in the UI. Cleanup is by
  retention only.

## Configuration

New `app.ini` section:

```ini
[repository.commit_status]
; instance master switch
ENABLED = true
; distinct contexts allowed per commit
MAX_CONTEXTS_PER_COMMIT = 20
; attempts kept per (commit, context) before the oldest are pruned
MAX_ATTEMPTS_PER_CONTEXT = 20
; rows older than this are pruned regardless of the per context cap
RETENTION_DAYS = 90
```

Add a cron entry in `internal/cron` that prunes `commit_status` in two passes,
using the existing cron framework:

1. Delete every row older than `RETENTION_DAYS`.
2. For each `(repo_id, commit_sha, context)` group, keep the most recent
   `MAX_ATTEMPTS_PER_CONTEXT` rows and delete the rest.

The per context cap bounds growth on a commit that is re-built many times, e.g.,
a long lived branch with a flaky pipeline, where a pure time window would still
accumulate thousands of rows.

## UI

### Tab

`web/src/components/RepoHeader.tsx` already centralizes the tab strip. Add
`"builds"` to the `RepoTab` union and an entry in `buildTabs()`, gated on
`repo.commitStatusEnabled`. Mirror the addition in the legacy
`templates/repo/header.tmpl` so the tab is present in both frontends during the
migration.

Tab placement: after "Pull requests", before "Wiki".

### Pages

SPA routes in `web/src/routes/`, pages in `web/src/pages/repo/`. Route
`loader`s fetch the data so the page mounts with data present, per the
repository UI guideline. No fetching from `useEffect`.

| Route                          | Page               | Content |
|--------------------------------|--------------------|---------|
| `/:owner/:repo/builds`         | `Builds.tsx`       | paginated list grouped by commit: context, state as icon plus color plus text, description, relative time, "open in Jenkins" link. Filters by branch and by state. |
| `/:owner/:repo/builds/:sha`    | `BuildDetail.tsx`  | every context for that SHA, each with its attempt history and description. No embedded logs. Logs live in Jenkins, reached through `target_url`. |

State is never conveyed by color alone. Each state has an icon and a text
label: pending is a hollow circle, running is a half circle, success is a
check, failure is a cross, error is a warning triangle. Contrast meets WCAG 2.2
AA. Targets are at least 24 by 24 CSS px. Tested at 375 px width before desktop
refinements.

Empty state, shown when the repository has no statuses: explain that builds
come from an external CI system, with a link to the Jenkins setup guide.
Without this the tab reads as broken.

### Commit list and repository home

Show the combined status badge in two places:

- Next to each SHA in the commit list: `web/src/pages/repo/Commit.tsx` and
  legacy `templates/repo/commits_table.tmpl`, linking to `BuildDetail` for that
  SHA.
- Next to the latest commit line in the repository home header, as GitHub does:
  the SPA home header component and legacy `templates/repo/home.tmpl`, linking
  to `BuildDetail` for the branch head.

The branches page is out of scope for the first PR.

### Pull request page

Add a "Checks" section above the merge button, listing the latest status per
context for the PR head commit, each row linking to its `target_url`. This
section is informational in this PR. It does not block merging. Blocking is
phase 3.

### Live updates

TanStack Query with `refetchInterval` while any visible status is `pending` or
`running`, stopping once everything is terminal. Server push is a later phase.
Gogs has no push infrastructure today.

## Jenkins integration

### Trigger, already works, documentation only

- Repository settings, Webhooks: add a Gogs webhook pointing at the Jenkins
  `gogs-webhook` endpoint, events `push` and `pull_request`.
- Jenkins job: Gogs plugin or Generic Webhook Trigger.

### Status callback, new, user side

1. Create a dedicated user, e.g., `jenkins-ci`, and add it as a write
   collaborator on each repository that has CI.
2. Generate a personal access token for that user.
3. In the `Jenkinsfile`, report transitions. With no dedicated plugin this is a
   `curl` in a `post` block:

```groovy
void gogsStatus(String state, String desc) {
  sh """
    curl -sS -X POST \
      -H 'Authorization: token ${env.GOGS_TOKEN}' \
      -H 'Content-Type: application/json' \
      -d '{"state":"${state}","context":"jenkins/build","target_url":"${env.BUILD_URL}","description":"${desc}"}' \
      ${env.GOGS_URL}/api/v1/repos/${env.REPO}/statuses/${env.GIT_COMMIT}
  """
}

pipeline {
  agent any
  stages {
    stage('notify') { steps { script { gogsStatus('pending', 'Build queued') } } }
    stage('build')  { steps { script { gogsStatus('running', 'Building'); sh 'make build' } } }
  }
  post {
    success  { script { gogsStatus('success', "Passed in ${currentBuild.durationString}") } }
    failure  { script { gogsStatus('failure', 'Build failed') } }
    unstable { script { gogsStatus('error',   'Tests unstable') } }
  }
}
```

Document this in `docs/advancing/` as a CI integration guide.

## Files touched, first PR

Go:

- `internal/database/commit_status.go` (new), `commit_status_test.go` (new)
- `internal/database/migrations/v23.go` (new) and an entry in `migrations.go`
- `internal/database/repo.go`, new `EnableCommitStatus` column
- `internal/database/webhook.go`, `status` event
- `internal/database/mocks_gen.go`, regenerated via `go generate ./...`
- `internal/route/api/v1/repo_status.go` (new), `types/status.go` (new)
- `internal/route/api/v1/api.go`, route registration
- `internal/route/repo/`, legacy tab handlers and the settings toggle
- `internal/conf/`, new config section and defaults
- `internal/cron/`, retention job
- `internal/context/`, a `RequireCommitStatus` guard if the legacy routes need one

Frontend:

- `web/src/components/RepoHeader.tsx`, new tab
- `web/src/routes/`, two new routes
- `web/src/pages/repo/Builds.tsx`, `BuildDetail.tsx` (new)
- `web/src/lib/queries/`, status queries
- `web/src/pages/repo/Commit.tsx`, combined status badge in the commit list
- repository home header component, combined status badge on the latest commit
- pull request page component, checks section

Legacy templates:

- `templates/repo/header.tmpl`, new tab
- `templates/repo/builds/` (new)
- `templates/repo/commits_table.tmpl`, badge in the commit list
- `templates/repo/home.tmpl`, badge on the latest commit line
- pull request template, checks section

Docs and locale:

- `conf/locale/locale_en-US.ini`, new keys, en-US only
- `docs/dev/database_schema.md`, regenerated
- `docs/advancing/`, Jenkins CI integration guide
- `CHANGELOG.md`, an "Added" entry describing the user visible impact only

## Resolved decisions

- Combined status badge appears in the commit list and on the repository home
  header next to the latest commit. The branches page is deferred.
- `running` is a distinct rung in the combined status precedence, above
  `pending` and below the failing states.
- Retention is a time window (`RETENTION_DAYS`) plus a per context attempt cap
  (`MAX_ATTEMPTS_PER_CONTEXT`), pruned by the cron job in two passes.
- The pull request checks section ships in this first PR.

## Open questions

- Whether the home header badge links to `BuildDetail` for the branch head or
  to the "Builds" tab filtered to that branch.
- Icon set and exact colors for the five states, to be settled against
  `web/DESIGN.md` during implementation.
