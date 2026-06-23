"use client";

import { useEffect, useState, useCallback } from "react";
import { useT } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { MemoryTestButton } from "@/components/memory-test-button";
import { Database, Boxes, Settings2, Check, Loader2 } from "lucide-react";
import {
  getConfig,
  updateConfig,
} from "@/lib/api";

interface MemoryEmbeddingConfig {
  enabled: boolean;
  provider: string;
  model: string;
  apiKey: string;
  apiBase: string;
  dim: number;
  dimEnabled?: boolean;
}

interface MemoryRerankerConfig {
  enabled: boolean;
  provider: string;
  model: string;
  apiKey: string;
  apiBase: string;
}

interface MemorySettingsConfig {
  enabled: boolean;
  reindexIntervalMin?: number;
}

interface MemoryConfig {
  embedding?: MemoryEmbeddingConfig;
  reranker?: MemoryRerankerConfig;
  settings?: MemorySettingsConfig;
  summaryModel?: string;
}

export default function MemoryPage() {
  const t = useT();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  const [embedding, setEmbedding] = useState<MemoryEmbeddingConfig>({
    enabled: false,
    provider: "",
    model: "",
    apiKey: "",
    apiBase: "",
    dim: 1024,
    dimEnabled: false,
  });

  const [reranker, setReranker] = useState<MemoryRerankerConfig>({
    enabled: false,
    provider: "",
    model: "",
    apiKey: "",
    apiBase: "",
  });

  const [settings, setSettings] = useState<MemorySettingsConfig>({
    enabled: true,
  });
  const [summaryModel, setSummaryModel] = useState("");

  const refresh = useCallback(async () => {
    try {
      const r = await getConfig();
      const mem = (r as unknown as Record<string, unknown>)?.memory as MemoryConfig | undefined;
      if (mem?.embedding) {
        const e = mem.embedding as MemoryEmbeddingConfig;
        setEmbedding({
          enabled: e.enabled ?? false,
          provider: e.provider || "",
          model: e.model || "",
          apiKey: e.apiKey || "",
          apiBase: e.apiBase || "",
          dim: e.dim || 1024,
          dimEnabled: e.dimEnabled ?? false,
        });
      }
      if (mem?.reranker) {
        const rr = mem.reranker as MemoryRerankerConfig;
        setReranker({
          enabled: rr.enabled ?? false,
          provider: rr.provider || "",
          model: rr.model || "",
          apiKey: rr.apiKey || "",
          apiBase: rr.apiBase || "",
        });
      }
      if (mem?.settings) {
        setSettings({
          enabled: mem.settings.enabled ?? true,
          reindexIntervalMin: mem.settings.reindexIntervalMin ?? 0,
        });
      }
      setSummaryModel(mem?.summaryModel || "");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const flashSaved = () => {
    setSaved(true);
    setTimeout(() => setSaved(false), 2000);
  };

  // Save persists the whole memory block at once. The config response
  // returns these keys in plaintext (no masking), so we round-trip the
  // loaded value; editing a toggle or URL never wipes a stored secret.
  const handleSave = async () => {
    setSaving(true);
    try {
      await updateConfig({
        memory: {
          embedding,
          reranker,
          settings,
          summaryModel,
        },
      });
      flashSaved();
      await refresh();
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="p-6 space-y-6 max-w-5xl mx-auto">
        <Skeleton className="h-10 w-48" />
        {[1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-32" />
        ))}
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6 max-w-5xl mx-auto">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-2xl font-semibold tracking-tight">{t("memory.title")}</h2>
          <p className="text-sm text-muted-foreground mt-1">{t("memory.subtitle")}</p>
        </div>
        <Button
          onClick={handleSave}
          disabled={saving}
          variant={saved ? "outline" : "default"}
          className={saved ? "border-emerald-500/30 text-emerald-600 dark:text-emerald-400" : ""}
        >
          {saved ? (
            <>
              <Check className="h-4 w-4 mr-2" />
              {t("common.saved")}
            </>
          ) : saving ? (
            <>
              <Loader2 className="h-4 w-4 mr-2 animate-spin" />
              {t("common.saving")}
            </>
          ) : (
            t("common.save")
          )}
        </Button>
      </div>

      {/* Settings — master switch */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <Settings2 className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <h3 className="font-medium">{t("memory.memorySettings")}</h3>
              <p className="text-sm text-muted-foreground mt-1">{t("memory.settingsDesc")}</p>
              <p className="text-xs text-muted-foreground/70 mt-1">{t("memory.enableRecall")}</p>
            </div>
          </div>
          <Switch
            checked={settings.enabled}
            onCheckedChange={(v: boolean) => setSettings({ ...settings, enabled: v })}
            aria-label={t("memory.memorySettings")}
          />
        </div>
        <div className="mt-4 pt-4 border-t border-border grid gap-4 sm:grid-cols-2">
          <div className="space-y-1.5">
            <Label>{t("memory.reindexInterval")}</Label>
            <Input
              type="number"
              min={0}
              value={settings.reindexIntervalMin ?? 0}
              onChange={(e) =>
                setSettings({ ...settings, reindexIntervalMin: parseInt(e.target.value) || 0 })
              }
              placeholder="10"
              className="font-mono text-sm"
            />
            <p className="text-xs text-muted-foreground/70">{t("memory.reindexIntervalDesc")}</p>
          </div>
          <div className="space-y-1.5">
            <Label>{t("memory.summaryModel")}</Label>
            <Input
              value={summaryModel}
              onChange={(e) => setSummaryModel(e.target.value)}
              placeholder="e.g. openai/gpt-4o-mini"
              className="font-mono text-sm"
            />
            <p className="text-xs text-muted-foreground/70">{t("memory.summaryModelDesc")}</p>
          </div>
        </div>
      </div>

      {/* Embedding */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <Database className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <h3 className="font-medium">{t("memory.embedding")}</h3>
                {embedding.enabled ? (
                  <Badge className="bg-emerald-500/15 text-emerald-700 hover:bg-emerald-500/15 text-[10px]">
                    {t("memory.configured")}
                  </Badge>
                ) : (
                  <Badge variant="outline" className="text-muted-foreground text-[10px]">
                    {t("memory.notConfigured")}
                  </Badge>
                )}
              </div>
              <p className="text-sm text-muted-foreground mt-1">{t("memory.embeddingDesc")}</p>
            </div>
          </div>
          <Switch
            checked={embedding.enabled}
            onCheckedChange={(v: boolean) => setEmbedding({ ...embedding, enabled: v })}
            aria-label={t("memory.embedding")}
          />
        </div>

        {embedding.enabled && (
          <div className="mt-5 pt-5 border-t border-border space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>{t("memory.provider")}</Label>
                <Input
                  value={embedding.provider}
                  onChange={(e) => setEmbedding({ ...embedding, provider: e.target.value })}
                  placeholder={t("memory.providerPlaceholder")}
                  className="font-mono text-sm"
                />
              </div>
              <div className="space-y-1.5">
                <Label>{t("memory.model")}</Label>
                <Input
                  value={embedding.model}
                  onChange={(e) => setEmbedding({ ...embedding, model: e.target.value })}
                  placeholder={t("memory.modelPlaceholder")}
                  className="font-mono text-sm"
                />
              </div>
            </div>
            <div className="space-y-1.5">
              <Label>{t("memory.apiBase")}</Label>
              <Input
                value={embedding.apiBase}
                onChange={(e) => setEmbedding({ ...embedding, apiBase: e.target.value })}
                placeholder={t("memory.apiBasePlaceholder")}
                className="font-mono text-sm"
              />
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>{t("memory.apiKey")}</Label>
                <Input
                  type="password"
                  value={embedding.apiKey}
                  onChange={(e) => setEmbedding({ ...embedding, apiKey: e.target.value })}
                  placeholder={t("memory.apiKeyPlaceholder")}
                  className="font-mono text-sm placeholder:text-muted-foreground/70"
                />
              </div>
              <div className="space-y-1.5">
                <Label>{t("memory.dimensions")}</Label>
                <div className="flex items-center gap-2">
                  <Input
                    type="number"
                    value={embedding.dim}
                    onChange={(e) =>
                      setEmbedding({ ...embedding, dim: parseInt(e.target.value) || 1024 })
                    }
                    placeholder="1024"
                    className="flex-1 font-mono text-sm"
                  />
                  <Switch
                    checked={!!embedding.dimEnabled}
                    onCheckedChange={(v) => setEmbedding({ ...embedding, dimEnabled: v })}
                    aria-label={t("memory.sendDimensions")}
                  />
                </div>
                <p className="text-xs text-muted-foreground/70">{t("memory.sendDimensions")}</p>
              </div>
            </div>
            <MemoryTestButton
              kind="embedding"
              apiBase={embedding.apiBase || ""}
              apiKey={embedding.apiKey || ""}
              model={embedding.model || ""}
              dim={embedding.dim}
              dimEnabled={embedding.dimEnabled}
            />
          </div>
        )}
      </div>

      {/* Reranker */}
      <div className="rounded-lg border border-border bg-card p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="flex items-start gap-3 min-w-0">
            <Boxes className="h-4 w-4 text-primary mt-0.5 shrink-0" />
            <div className="min-w-0">
              <div className="flex items-center gap-2">
                <h3 className="font-medium">{t("memory.reranker")}</h3>
                {reranker.enabled ? (
                  <Badge className="bg-emerald-500/15 text-emerald-700 hover:bg-emerald-500/15 text-[10px]">
                    {t("memory.configured")}
                  </Badge>
                ) : (
                  <Badge variant="outline" className="text-muted-foreground text-[10px]">
                    {t("memory.notConfigured")}
                  </Badge>
                )}
              </div>
              <p className="text-sm text-muted-foreground mt-1">{t("memory.rerankerDesc")}</p>
            </div>
          </div>
          <Switch
            checked={reranker.enabled}
            onCheckedChange={(v: boolean) => setReranker({ ...reranker, enabled: v })}
            aria-label={t("memory.reranker")}
          />
        </div>

        {reranker.enabled && (
          <div className="mt-5 pt-5 border-t border-border space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>{t("memory.provider")}</Label>
                <Input
                  value={reranker.provider}
                  onChange={(e) => setReranker({ ...reranker, provider: e.target.value })}
                  placeholder={t("memory.providerPlaceholder")}
                  className="font-mono text-sm"
                />
              </div>
              <div className="space-y-1.5">
                <Label>{t("memory.model")}</Label>
                <Input
                  value={reranker.model}
                  onChange={(e) => setReranker({ ...reranker, model: e.target.value })}
                  placeholder={t("memory.rerankerModelPlaceholder")}
                  className="font-mono text-sm"
                />
              </div>
            </div>
            <div className="space-y-1.5">
              <Label>{t("memory.apiBase")}</Label>
              <Input
                value={reranker.apiBase}
                onChange={(e) => setReranker({ ...reranker, apiBase: e.target.value })}
                placeholder={t("memory.rerankerApiBasePlaceholder")}
                className="font-mono text-sm"
              />
            </div>
            <div className="space-y-1.5">
              <Label>{t("memory.apiKey")}</Label>
              <Input
                type="password"
                value={reranker.apiKey}
                onChange={(e) => setReranker({ ...reranker, apiKey: e.target.value })}
                placeholder={t("memory.apiKeyPlaceholder")}
                className="font-mono text-sm placeholder:text-muted-foreground/70"
              />
            </div>
            <MemoryTestButton
              kind="reranker"
              apiBase={reranker.apiBase || ""}
              apiKey={reranker.apiKey || ""}
              model={reranker.model || ""}
            />
          </div>
        )}
      </div>
    </div>
  );
}
