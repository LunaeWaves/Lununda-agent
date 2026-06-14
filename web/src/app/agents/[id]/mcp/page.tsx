"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Cable, Pencil, Plus, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { useT } from "@/lib/i18n";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";
import {
  getAgent,
  updateAgent,
  type MCPServerConfig,
} from "@/lib/api";

// Per-agent MCP server overlay. Each row in agents.defaults.mcpServers
// becomes a Manager-attached MCP client at agent-boot — see
// internal/agent/loop.go (mcp.NewManager). Whole-map replace semantics
// on save: we read the current map, mutate locally, and PUT the full
// thing back so the backend's `mcpServers` patch overwrites the prior
// row. stdio servers launch a local subprocess (command + args + env);
// http servers connect to a remote URL (with optional headers).
type ServerType = "stdio" | "http";

interface FormState {
  name: string;
  type: ServerType;
  url: string;
  headers: string; // KEY=VALUE per line
  command: string;
  args: string; // one arg per line
  env: string; // KEY=VALUE per line
}

const EMPTY_FORM: FormState = {
  name: "",
  type: "stdio",
  url: "",
  headers: "",
  command: "",
  args: "",
  env: "",
};

function parseKVLines(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const eq = trimmed.indexOf("=");
    if (eq <= 0) continue;
    const k = trimmed.slice(0, eq).trim();
    const v = trimmed.slice(eq + 1).trim();
    if (k) out[k] = v;
  }
  return out;
}

function formatKVLines(map?: Record<string, string>): string {
  if (!map) return "";
  return Object.entries(map)
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

function serverToForm(name: string, s: MCPServerConfig): FormState {
  return {
    name,
    type: (s.type === "http" ? "http" : "stdio") as ServerType,
    url: s.url ?? "",
    headers: formatKVLines(s.headers),
    command: s.command ?? "",
    args: (s.args ?? []).join("\n"),
    env: formatKVLines(s.env),
  };
}

function formToServer(f: FormState): MCPServerConfig {
  const args = f.args
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
  const server: MCPServerConfig = { type: f.type };
  if (f.type === "http") {
    if (f.url) server.url = f.url;
    const headers = parseKVLines(f.headers);
    if (Object.keys(headers).length) server.headers = headers;
  } else {
    if (f.command) server.command = f.command;
    if (args.length) server.args = args;
    const env = parseKVLines(f.env);
    if (Object.keys(env).length) server.env = env;
  }
  return server;
}

export default function AgentMcpPage() {
  const t = useT();
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  const [servers, setServers] = useState<Record<string, MCPServerConfig>>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [editingKey, setEditingKey] = useState<string | null>(null); // existing server name being edited, or null
  const [isNew, setIsNew] = useState(false);
  const [form, setForm] = useState<FormState>(EMPTY_FORM);
  const [error, setError] = useState<string | null>(null);

  const fetchAgent = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const rec = await getAgent(agentId).catch(() => null);
      setServers(rec?.mcpServers ?? {});
    } finally {
      setLoading(false);
    }
  }, [agentId]);

  useEffect(() => {
    fetchAgent();
  }, [fetchAgent]);

  const sortedNames = useMemo(
    () => Object.keys(servers).sort((a, b) => a.localeCompare(b)),
    [servers],
  );

  const startAdd = () => {
    setIsNew(true);
    setEditingKey(null);
    setForm(EMPTY_FORM);
    setError(null);
  };

  const startEdit = (name: string) => {
    setIsNew(false);
    setEditingKey(name);
    setForm(serverToForm(name, servers[name]));
    setError(null);
  };

  const cancelForm = () => {
    setIsNew(false);
    setEditingKey(null);
    setForm(EMPTY_FORM);
    setError(null);
  };

  const persist = async (next: Record<string, MCPServerConfig>) => {
    setSaving(true);
    setError(null);
    try {
      await updateAgent(agentId, { mcpServers: next });
      setServers(next);
      cancelForm();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  const onSubmit = async () => {
    const name = form.name.trim();
    if (!name) {
      setError(t("mcp.error.nameRequired"));
      return;
    }
    if (form.type === "stdio" && !form.command.trim()) {
      setError(t("mcp.error.commandRequired"));
      return;
    }
    if (form.type === "http" && !form.url.trim()) {
      setError(t("mcp.error.urlRequired"));
      return;
    }
    // Renaming or adding with a name that already exists and isn't the one we're editing.
    if (isNew && servers[name]) {
      setError(t("mcp.error.duplicateName"));
      return;
    }
    if (!isNew && editingKey && editingKey !== name && servers[name]) {
      setError(t("mcp.error.duplicateName"));
      return;
    }
    const next: Record<string, MCPServerConfig> = {};
    for (const [k, v] of Object.entries(servers)) {
      if (k === editingKey) continue;
      next[k] = v;
    }
    next[name] = formToServer(form);
    await persist(next);
  };

  const onDelete = async (name: string) => {
    const next: Record<string, MCPServerConfig> = {};
    for (const [k, v] of Object.entries(servers)) {
      if (k === name) continue;
      next[k] = v;
    }
    await persist(next);
  };

  if (loading) {
    return (
      <div className="p-6 space-y-6 max-w-3xl mx-auto">
        <Skeleton className="h-10 w-48" />
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }

  const formOpen = isNew || editingKey !== null;

  return (
    <div className="p-6 space-y-6 max-w-3xl mx-auto">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">
            {t("mcp.title")}
          </h2>
          <p className="text-sm text-muted-foreground mt-1">
            {t("mcp.subtitle")} <strong>{agentName}</strong>.{" "}
            {t("mcp.subtitleSuffix")}
          </p>
        </div>
        {!formOpen && (
          <Button onClick={startAdd} size="sm">
            <Plus className="size-4 mr-1" />
            {t("mcp.add")}
          </Button>
        )}
      </div>

      {sortedNames.length === 0 && !formOpen ? (
        <div className="rounded-lg border border-dashed border-border bg-card/30 p-12">
          <div className="flex flex-col items-center justify-center">
            <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10 mb-4">
              <Cable className="h-7 w-7 text-primary" />
            </div>
            <p className="text-sm text-muted-foreground mb-1">
              {t("mcp.empty")}
            </p>
            <p className="text-xs text-muted-foreground/60 max-w-sm text-center">
              {t("mcp.emptyHint")}
            </p>
          </div>
        </div>
      ) : (
        <div className="space-y-3">
          {sortedNames.map((name) => (
            <div
              key={name}
              className="rounded-lg border border-border bg-card p-4"
            >
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <p className="text-sm font-medium truncate">{name}</p>
                    <span className="text-[10px] uppercase tracking-wide rounded bg-muted px-1.5 py-0.5 text-muted-foreground">
                      {servers[name].type}
                    </span>
                  </div>
                  <code className="text-[11px] text-muted-foreground mt-1 block break-all">
                    {servers[name].type === "http"
                      ? servers[name].url
                      : [servers[name].command, ...(servers[name].args ?? [])].join(" ")}
                  </code>
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => startEdit(name)}
                    aria-label={t("mcp.edit")}
                  >
                    <Pencil className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => onDelete(name)}
                    disabled={saving}
                    aria-label={t("mcp.delete")}
                  >
                    <Trash2 className="size-4 text-destructive" />
                  </Button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      {formOpen && (
        <div className="rounded-lg border border-border bg-card p-5 space-y-4">
          <h3 className="text-sm font-semibold">
            {isNew ? t("mcp.addTitle") : t("mcp.editTitle")}
          </h3>

          <div className="space-y-2">
            <Label htmlFor="mcp-name">{t("mcp.field.name")}</Label>
            <Input
              id="mcp-name"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              placeholder={t("mcp.field.namePlaceholder")}
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="mcp-type">{t("mcp.field.type")}</Label>
            <Select
              value={form.type}
              onValueChange={(v) => setForm({ ...form, type: v as ServerType })}
            >
              <SelectTrigger id="mcp-type">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="stdio">stdio</SelectItem>
                <SelectItem value="http">http</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {form.type === "stdio" ? (
            <>
              <div className="space-y-2">
                <Label htmlFor="mcp-command">{t("mcp.field.command")}</Label>
                <Input
                  id="mcp-command"
                  value={form.command}
                  onChange={(e) => setForm({ ...form, command: e.target.value })}
                  placeholder="npx"
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="mcp-args">{t("mcp.field.args")}</Label>
                <Textarea
                  id="mcp-args"
                  value={form.args}
                  onChange={(e) => setForm({ ...form, args: e.target.value })}
                  placeholder={"-y\n@modelcontextprotocol/server-filesystem\n/tmp"}
                  rows={3}
                  className="font-mono text-xs"
                />
                <p className="text-[11px] text-muted-foreground">
                  {t("mcp.field.argsHint")}
                </p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="mcp-env">{t("mcp.field.env")}</Label>
                <Textarea
                  id="mcp-env"
                  value={form.env}
                  onChange={(e) => setForm({ ...form, env: e.target.value })}
                  placeholder={"API_KEY=xxx\nNODE_ENV=production"}
                  rows={3}
                  className="font-mono text-xs"
                />
                <p className="text-[11px] text-muted-foreground">
                  {t("mcp.field.envHint")}
                </p>
              </div>
            </>
          ) : (
            <>
              <div className="space-y-2">
                <Label htmlFor="mcp-url">{t("mcp.field.url")}</Label>
                <Input
                  id="mcp-url"
                  value={form.url}
                  onChange={(e) => setForm({ ...form, url: e.target.value })}
                  placeholder="https://mcp.example.com/sse"
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="mcp-headers">{t("mcp.field.headers")}</Label>
                <Textarea
                  id="mcp-headers"
                  value={form.headers}
                  onChange={(e) => setForm({ ...form, headers: e.target.value })}
                  placeholder={"Authorization=Bearer xxx"}
                  rows={3}
                  className="font-mono text-xs"
                />
                <p className="text-[11px] text-muted-foreground">
                  {t("mcp.field.headersHint")}
                </p>
              </div>
            </>
          )}

          {error && (
            <p className="text-xs text-destructive">{error}</p>
          )}

          <div className="flex items-center justify-end gap-2 pt-2">
            <Button variant="outline" onClick={cancelForm} disabled={saving}>
              {t("mcp.cancel")}
            </Button>
            <Button onClick={onSubmit} disabled={saving}>
              {saving ? t("mcp.saving") : t("mcp.save")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
