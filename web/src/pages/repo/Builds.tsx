import { useSuspenseQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { ExternalLink } from "lucide-react";
import { useTranslation } from "react-i18next";

import { BuildStatePill } from "@/components/BuildStatePill";
import { RepoHeader } from "@/components/RepoHeader";
import { usePageTitle } from "@/lib/page-title";
import { type BuildGroup, repoBuildsQuery, repoHeaderQuery } from "@/lib/queries/repo";
import { formatRelativeTime } from "@/lib/relative-time";
import { subUrl } from "@/lib/url";

const ROUTE_ID = "/$owner/$repo/builds";

export function RepoBuilds() {
  const { owner, repo } = useParams({ from: ROUTE_ID });
  const { t } = useTranslation();
  const { data: header } = useSuspenseQuery(repoHeaderQuery(owner, repo));
  const { data: builds } = useSuspenseQuery(repoBuildsQuery(owner, repo));

  usePageTitle(`${t("repo.builds")} - ${owner}/${repo}`);

  return (
    <>
      <RepoHeader repo={header} activeTab="builds" />
      <section className="mx-auto w-full max-w-5xl px-4 pt-6 pb-10 sm:px-6">
        <h1 className="mb-4 text-lg font-semibold text-(--color-foreground)">{t("repo.builds")}</h1>

        {builds.groups.length === 0 ? (
          <EmptyState
            title={t("repo.builds.empty_title")}
            description={t("repo.builds.empty_desc")}
            settingsHref={header.viewerCanAdminister ? subUrl(`/${owner}/${repo}/settings`) : undefined}
            settingsLabel={t("repo.settings")}
          />
        ) : (
          <ul className="divide-y divide-(--color-border) overflow-hidden rounded-lg border border-(--color-border)">
            {builds.groups.map((group) => (
              <BuildGroupRow key={group.sha} owner={owner} repo={repo} group={group} />
            ))}
          </ul>
        )}
      </section>
    </>
  );
}

function BuildGroupRow({ owner, repo, group }: { owner: string; repo: string; group: BuildGroup }) {
  const { t } = useTranslation();
  const detailHref = subUrl(`/${owner}/${repo}/builds/${group.sha}`);
  const shortSha = group.sha.slice(0, 10);

  return (
    <li className="flex flex-col gap-2 bg-(--color-background) px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex min-w-0 items-center gap-3">
        <BuildStatePill state={group.state} size="sm" />
        <a href={detailHref} className="font-mono text-sm text-(--color-primary) hover:underline">
          {shortSha}
        </a>
        <span className="truncate text-xs text-(--color-muted-foreground)">
          {group.statuses.map((s) => s.context).join(", ")}
        </span>
      </div>
      <div className="flex shrink-0 flex-wrap items-center gap-3">
        {group.statuses.map((status) => (
          <span key={status.id} className="inline-flex items-center gap-1.5">
            <BuildStatePill state={status.state} size="sm" />
            <span className="text-xs text-(--color-muted-foreground)">{status.context}</span>
            {status.creator ? (
              <span className="text-xs text-(--color-muted-foreground)">· {status.creator}</span>
            ) : null}
            {status.targetURL ? (
              <a
                href={status.targetURL}
                target="_blank"
                rel="noopener noreferrer"
                className="text-(--color-muted-foreground) hover:text-(--color-foreground)"
                aria-label={t("repo.builds.view_on_ci")}
              >
                <ExternalLink className="size-3.5" aria-hidden />
              </a>
            ) : null}
          </span>
        ))}
        {group.statuses[0] ? (
          <time className="text-xs text-(--color-muted-foreground)" dateTime={group.statuses[0].created}>
            {formatRelativeTime(t, group.statuses[0].created)}
          </time>
        ) : null}
      </div>
    </li>
  );
}

function EmptyState({
  title,
  description,
  settingsHref,
  settingsLabel,
}: {
  title: string;
  description: string;
  settingsHref?: string;
  settingsLabel: string;
}) {
  return (
    <div className="rounded-lg border border-dashed border-(--color-border) px-6 py-12 text-center">
      <p className="text-sm font-medium text-(--color-foreground)">{title}</p>
      <p className="mx-auto mt-1 max-w-md text-xs text-(--color-muted-foreground)">{description}</p>
      {settingsHref ? (
        <a href={settingsHref} className="mt-3 inline-block text-xs text-(--color-primary) hover:underline">
          {settingsLabel}
        </a>
      ) : null}
    </div>
  );
}
