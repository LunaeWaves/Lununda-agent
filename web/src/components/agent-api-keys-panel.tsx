"use client";

import * as React from "react";
import { useT } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Check, Copy, KeyIcon, Plus, Trash2Icon } from "lucide-react";
import {
  createApikey,
  deleteApikey,
  listApikeys,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";

interface ApikeyRow {
  id: string;
  type: string;
  agents?: string[] | null;
  agentIds?: string[] | null;
  createdAt?: string;
  keyPreview?: string;
}

// AgentApiKeysPanel lists and mints agent-scoped apikeys (spec D1).
// Each key is scoped to this agent via apikey_agents ACL — the holder
// can call /v1/chat/completions as the owner. The plaintext key is
// shown once on creation; afterwards only a masked preview is kept.
export default function AgentApiKeysPanel() {
  const t = useT();
  const agentId = useAgentIdFromURL();
  const [keys, setKeys] = React.useState<ApikeyRow[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [busy, setBusy] = React.useState(false);
  const [newKey, setNewKey] = React.useState<string | null>(null);
  const [copied, setCopied] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  const refresh = React.useCallback(() => {
    if (!agentId) return;
    setLoading(true);
    listApikeys()
      .then((data: any) => {
        const all: ApikeyRow[] = data.apikeys || data.keys || [];
        // 只显示 scoped 到这个 agent 的 agent-type key
        setKeys(
          all.filter(
            (k) =>
              k.type === "agent" &&
              (k.agents || k.agentIds || []).includes(agentId),
          ),
        );
      })
      .catch(() => setKeys([]))
      .finally(() => setLoading(false));
  }, [agentId]);

  React.useEffect(() => {
    refresh();
  }, [refresh]);

  const onCreate = async () => {
    if (!agentId) return;
    setBusy(true);
    setError(null);
    try {
      const resp: any = await createApikey({
        name: `agent-${agentId.slice(0, 8)}`,
        type: "agent",
        agentIds: [agentId],
      });
      if (resp?.apikey?.key || resp?.token) {
        setNewKey(resp.apikey?.key || resp.token);
      }
      refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "create failed");
    } finally {
      setBusy(false);
    }
  };

  const onRevoke = async (id: string) => {
    setBusy(true);
    setError(null);
    try {
      await deleteApikey(id);
      refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : "delete failed");
    } finally {
      setBusy(false);
    }
  };

  const copyNew = async () => {
    if (!newKey) return;
    try {
      await navigator.clipboard.writeText(newKey);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // clipboard blocked
    }
  };

  return (
    <div className="p-6 max-w-3xl space-y-4">
      <div>
        <h2 className="text-lg font-semibold flex items-center gap-2">
          <KeyIcon className="h-5 w-5" />
          {t("settings.apiKeys")}
        </h2>
        <p className="text-sm text-muted-foreground mt-1">
          {t("apikeys.agentScopedDesc")}
        </p>
      </div>

      {newKey && (
        <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4 space-y-2">
          <p className="text-sm font-medium">{t("apikeys.copyNow")}</p>
          <div className="flex gap-2">
            <Input readOnly value={newKey} onFocus={(e) => e.currentTarget.select()} className="font-mono text-xs" />
            <Button variant="outline" onClick={copyNew}>
              {copied ? <Check className="h-4 w-4 mr-1.5" /> : <Copy className="h-4 w-4 mr-1.5" />}
              {copied ? t("common.copied") : t("common.copy")}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">{t("apikeys.copyNowWarn")}</p>
        </div>
      )}

      <Button onClick={onCreate} disabled={busy || !agentId}>
        <Plus className="h-4 w-4 mr-2" />
        {t("apikeys.create")}
      </Button>

      {error && <p className="text-sm text-destructive">{error}</p>}

      <div className="space-y-2">
        {loading ? (
          <p className="text-sm text-muted-foreground">…</p>
        ) : keys.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("apikeys.empty")}</p>
        ) : (
          keys.map((k) => (
            <div
              key={k.id}
              className="flex items-center justify-between rounded-lg border px-3 py-2 text-sm"
            >
              <div>
                <p className="font-mono text-xs">{k.id}</p>
                {k.createdAt && (
                  <p className="text-xs text-muted-foreground">{k.createdAt}</p>
                )}
              </div>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => onRevoke(k.id)}
                disabled={busy}
                className="text-destructive hover:text-destructive"
              >
                <Trash2Icon className="h-3.5 w-3.5" />
              </Button>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
