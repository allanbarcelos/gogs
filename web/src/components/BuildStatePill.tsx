import { useTranslation } from "react-i18next";

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { buildStateAppearance } from "@/lib/build-status";
import type { CommitStatusState } from "@/lib/queries/repo";
import { cn } from "@/lib/utils";

interface BuildStatePillProps {
  state: CommitStatusState | "";
  // "sm" is icon-only (with an accessible label); "md" shows the text too.
  size?: "sm" | "md";
  className?: string;
}

export function BuildStatePill({ state, size = "md", className }: BuildStatePillProps) {
  const { t } = useTranslation();
  const { labelKey, Icon, className: toneClass, spin } = buildStateAppearance(state);
  const label = t(labelKey);

  if (size === "sm") {
    return (
      <Tooltip>
        <TooltipTrigger asChild>
          <span className={cn("inline-flex size-6 items-center justify-center", toneClass, className)} aria-label={label}>
            <Icon className={cn("size-4", spin && "animate-spin")} aria-hidden />
          </span>
        </TooltipTrigger>
        <TooltipContent>{label}</TooltipContent>
      </Tooltip>
    );
  }

  return (
    <span className={cn("inline-flex items-center gap-1.5 text-xs font-medium", toneClass, className)}>
      <Icon className={cn("size-4 shrink-0", spin && "animate-spin")} aria-hidden />
      <span>{label}</span>
    </span>
  );
}
