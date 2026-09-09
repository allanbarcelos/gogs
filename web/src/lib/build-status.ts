import { AlertTriangle, Check, Circle, CircleDashed, Loader2, X } from "lucide-react";
import type { ComponentType } from "react";

import type { CommitStatusState } from "@/lib/queries/repo";

export interface BuildStateAppearance {
  // i18n key under `builds.*` for the human label.
  labelKey: string;
  Icon: ComponentType<{ className?: string; "aria-hidden"?: boolean }>;
  // Tailwind text-color utility for the icon and label. Never the only signal:
  // the label text and icon shape always accompany it.
  className: string;
  spin?: boolean;
}

const APPEARANCE: Record<CommitStatusState, BuildStateAppearance> = {
  success: { labelKey: "repo.builds.state_success", Icon: Check, className: "text-(--color-success)" },
  failure: { labelKey: "repo.builds.state_failure", Icon: X, className: "text-(--color-destructive)" },
  error: { labelKey: "repo.builds.state_error", Icon: AlertTriangle, className: "text-(--color-destructive)" },
  running: {
    labelKey: "repo.builds.state_running",
    Icon: Loader2,
    className: "text-(--color-warning,oklch(0.795_0.184_86.047))",
    spin: true,
  },
  pending: { labelKey: "repo.builds.state_pending", Icon: Circle, className: "text-(--color-muted-foreground)" },
};

const UNKNOWN: BuildStateAppearance = {
  labelKey: "repo.builds.state_pending",
  Icon: CircleDashed,
  className: "text-(--color-muted-foreground)",
};

export function buildStateAppearance(state: string): BuildStateAppearance {
  return APPEARANCE[state as CommitStatusState] ?? UNKNOWN;
}
