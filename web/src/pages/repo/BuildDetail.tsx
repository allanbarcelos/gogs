import { useSuspenseQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { ExternalLink } from "lucide-react";
import { useTranslation } from "react-i18next";

import { BuildStatePill } from "@/components/BuildStatePill";
import { RepoHeader } from "@/components/RepoHeader";
import { usePageTitle } from "@/lib/page-title";
import { type BuildStatus, repoCommitStatusesQuery, repoHeaderQuery } from "@/lib/queries/repo";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relative-time";
import { subUrl } from "@/lib/url";

const ROUTE_ID = "/$owner/$repo/builds/$sha";

export function RepoBuildDetail() {
  const { owner, repo, sha } = useParams({ from: ROUTE_ID });
  const { t } = useTranslation();
  const { data: header } = useSuspenseQuery(repoHeaderQuery(owner, repo));
  const { data: statuses } = useSuspenseQuery(repoCommitStatusesQuery(owner, repo, sha));

  const shortSha = statuses.sha.slice(0, 10);
  usePageTitle(`${t("repo.builds")} ${shortSha} - ${owner}/${repo}`);

  return (
    <>
      <RepoHeader repo={header} activeTab="builds" />
      <section className="mx-auto w-full max-w-3xl px-4 pt-6 pb-10 sm:px-6">
        <h1 className="mb-1 flex flex-wrap items-center gap-2 text-lg font-semibold text-(--color-foreground)">
          <span>{t("repo.builds.for_commit")}</span>
          <a
            href={subUrl(`/${owner}/${repo}/commit/${statuses.sha}`)}
            className="font-mono text-base text-(--color-primary) hover:underline"
          >
            {shortSha}
          </a>
        </h1>
        <div className="mb-6">
          <BuildStatePill state={statuses.state} />
        </div>

        {statuses.latest.length === 0 ? (
          <p className="text-sm text-(--color-muted-foreground)">{t("repo.builds.empty_desc")}</p>
        ) : (
          <ul className="divide-y divide-(--color-border) overflow-hidden rounded-lg border border-(--color-border)">
            {statuses.latest.map((status) => (
              <StatusRow key={status.id} status={status} />
            ))}
          </ul>
        )}

        {statuses.attempts.length > statuses.latest.length ? (
          <details className="mt-6">
            <summary className="cursor-pointer text-sm font-medium text-(--color-foreground)">
              {t("repo.builds.attempts")} ({statuses.attempts.length})
            </summary>
            <ul className="mt-2 divide-y divide-(--color-border) overflow-hidden rounded-lg border border-(--color-border)">
              {statuses.attempts.map((status) => (
                <StatusRow key={status.id} status={status} />
              ))}
            </ul>
          </details>
        ) : null}
      </section>
    </>
  );
}

function StatusRow({ status }: { status: BuildStatus }) {
  const { t } = useTranslation();
  return (
    <li className="flex flex-col gap-1 bg-(--color-background) px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex min-w-0 items-center gap-3">
        <BuildStatePill state={status.state} size="sm" />
        <span className="font-medium text-(--color-foreground)">{status.context}</span>
        {status.description ? (
          <span className="truncate text-xs text-(--color-muted-foreground)">{status.description}</span>
        ) : null}
      </div>
      <div className="flex shrink-0 items-center gap-3">
        <time
          className="text-xs text-(--color-muted-foreground)"
          dateTime={status.created}
          title={formatAbsoluteTime(status.created)}
        >
          {formatRelativeTime(t, status.created)}
        </time>
        {status.targetURL ? (
          <a
            href={status.targetURL}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-xs text-(--color-primary) hover:underline"
          >
            {t("repo.builds.view_on_ci")}
            <ExternalLink className="size-3.5" aria-hidden />
          </a>
        ) : null}
      </div>
    </li>
  );
}
