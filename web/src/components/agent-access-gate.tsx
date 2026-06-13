"use client";

import { useEffect, useState } from "react";
import { usePathname } from "next/navigation";
import { Bot } from "lucide-react";
import { getAgentStatus } from "@/lib/api";
import { useT } from "@/lib/i18n";

function agentIdFromPath(pathname: string | null | undefined): string {
  if (!pathname) return "";
  const m = pathname.match(/^\/agents\/([^/]+)/);
  return m ? m[1] : "";
}

export default function AgentAccessGate({
  children,
}: {
  children: React.ReactNode;
}) {
  const pathname = usePathname();
  const agentId = agentIdFromPath(pathname);
  const [state, setState] = useState<"checking" | "ok" | "denied">("checking");
  const t = useT();

  useEffect(() => {
    if (!agentId || agentId === "default") {
      setState("ok");
      return;
    }
    let aborted = false;
    setState("checking");
    getAgentStatus(agentId)
      .then(({ status, agent }) => {
        if (aborted) return;
        if (status === 200 && agent) {
          setState("ok");
          return;
        }
        setState("denied");
      })
      .catch(() => {
        if (!aborted) setState("denied");
      });
    return () => {
      aborted = true;
    };
  }, [agentId]);

  if (state === "checking") {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-background">
        <div className="h-2 w-2 animate-pulse rounded-full bg-muted-foreground/40" />
      </div>
    );
  }

  if (state === "denied") {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-background p-6">
        <div className="max-w-md text-center space-y-4">
          <div className="mx-auto flex h-14 w-14 items-center justify-center rounded-2xl bg-muted/60">
            <Bot className="h-7 w-7 text-muted-foreground" />
          </div>
          <h2 className="text-lg font-semibold">{t("gate.noAccess")}</h2>
          <p className="text-sm text-muted-foreground">
            {t("gate.noAccessDesc")}
          </p>
        </div>
      </div>
    );
  }

  return <>{children}</>;
}
