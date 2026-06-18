"use client";
import { useT } from "@/lib/i18n";

import { useEffect, useState, useCallback } from "react";
import {
  getConfig,
  updateConfig,
  type ConfigResponse,
} from "@/lib/api";

interface MemoryEmbeddingConfig {
  enabled: boolean;
  provider: string;
  model: string;
  apiKey: string;
  apiBase: string;
  dim: number;
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
}

interface MemoryConfig {
  embedding?: MemoryEmbeddingConfig;
  reranker?: MemoryRerankerConfig;
  settings?: MemorySettingsConfig;
  [key: string]: unknown;
}

export default function MemoryPage() {
  const t = useT();
  const [config, setConfig] = useState<ConfigResponse | null>(null);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);

  // Extract memory sub-config or defaults
  const mem = config as unknown as Record<string, unknown>;
  const memory = mem?.memory as MemoryConfig | undefined;

  const [embedding, setEmbedding] = useState<MemoryEmbeddingConfig>({
    enabled: false,
    provider: "",
    model: "",
    apiKey: "",
    apiBase: "",
    dim: 1024,
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

  const rekey = useCallback((key: string) => {
    // Keep existing API key placeholder
  }, []);

  const refresh = useCallback(async () => {
    setError("");
    try {
      const r = await getConfig();
      setConfig(r);
      const mem = r as unknown as Record<string, unknown>;
      const mm = mem?.memory as MemoryConfig | undefined;
      if (mm?.embedding) {
        setEmbedding({
          enabled: mm.embedding.enabled ?? false,
          provider: mm.embedding.provider || "",
          model: mm.embedding.model || "",
          apiKey: "",
          apiBase: mm.embedding.apiBase || "",
          dim: mm.embedding.dim || 1024,
        });
      }
      if (mm?.reranker) {
        setReranker({
          enabled: mm.reranker.enabled ?? false,
          provider: mm.reranker.provider || "",
          model: mm.reranker.model || "",
          apiKey: "",
          apiBase: mm.reranker.apiBase || "",
        });
      }
      if (mm?.settings) {
        setSettings({
          enabled: mm.settings.enabled ?? true,
        });
      }
    } catch (e) {
      setError(String(e));
    }
  }, []);

  useEffect(() => { refresh(); }, [refresh]);

  async function handleSave() {
    setSaving(true);
    setError("");
    try {
      const body: Record<string, unknown> = {
        memory: {
          embedding: {
            ...embedding,
            apiKey: embedding.apiKey || undefined,
          },
          reranker: {
            ...reranker,
            apiKey: reranker.apiKey || undefined,
          },
          settings,
        },
      };
      await updateConfig(body);
      setDirty(false);
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  }

  const markDirty = (fn: () => void) => {
    fn();
    setDirty(true);
  };

  return (
    <div className="p-8 text-zinc-100">
      <h1 className="mb-2 text-2xl font-bold">{t("memory.title") || "Memory"}</h1>
      <p className="mb-6 text-sm text-zinc-500">
        {t("memory.subtitle") || "Configure semantic memory — embedding for vector recall and reranker for cross-encoder re-rank."}
      </p>

      {error && <p className="mb-4 text-sm text-destructive">{error}</p>}

      {/* Embedding */}
      <section className="mb-8 rounded-lg border border-zinc-800 bg-zinc-900 p-6">
        <h2 className="mb-1 font-semibold">{t("memory.embedding") || "Embedding Provider"}</h2>
        <p className="mb-4 text-sm text-zinc-500">
          {t("memory.embeddingDesc") || "Vectorize conversation summaries for semantic recall. Any OpenAI-compatible /v1/embeddings endpoint works."}
        </p>
        <label className="mb-4 flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={embedding.enabled}
            onChange={(e) => markDirty(() => setEmbedding({ ...embedding, enabled: e.target.checked }))}
            className="rounded"
          />
          {t("memory.enabled") || "Enabled"}
        </label>
        {embedding.enabled && (
          <div className="grid grid-cols-2 gap-3">
            <input
              value={embedding.provider}
              onChange={(e) => markDirty(() => setEmbedding({ ...embedding, provider: e.target.value }))}
              placeholder={t("memory.providerPlaceholder") || "openai"}
              className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm"
            />
            <input
              value={embedding.model}
              onChange={(e) => markDirty(() => setEmbedding({ ...embedding, model: e.target.value }))}
              placeholder={t("memory.modelPlaceholder") || "text-embedding-3-small"}
              className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm"
            />
            <input
              type="password"
              value={embedding.apiKey}
              onChange={(e) => markDirty(() => setEmbedding({ ...embedding, apiKey: e.target.value }))}
              placeholder={t("memory.apiKeyPlaceholder") || "API key"}
              className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm"
            />
            <input
              value={embedding.apiBase}
              onChange={(e) => markDirty(() => setEmbedding({ ...embedding, apiBase: e.target.value }))}
              placeholder={t("memory.apiBasePlaceholder") || "https://api.openai.com/v1"}
              className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm"
            />
            <input
              type="number"
              value={embedding.dim}
              onChange={(e) => markDirty(() => setEmbedding({ ...embedding, dim: parseInt(e.target.value) || 1024 }))}
              placeholder="1024"
              className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm"
            />
          </div>
        )}
      </section>

      {/* Reranker */}
      <section className="mb-8 rounded-lg border border-zinc-800 bg-zinc-900 p-6">
        <h2 className="mb-1 font-semibold">{t("memory.reranker") || "Reranker Provider"}</h2>
        <p className="mb-4 text-sm text-zinc-500">
          {t("memory.rerankerDesc") || "Cross-encoder that re-ranks coarse retrieval candidates. Jina AI or Cohere /v1/rerank compatible endpoints."}
        </p>
        <label className="mb-4 flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={reranker.enabled}
            onChange={(e) => markDirty(() => setReranker({ ...reranker, enabled: e.target.checked }))}
            className="rounded"
          />
          {t("memory.enabled") || "Enabled"}
        </label>
        {reranker.enabled && (
          <div className="grid grid-cols-2 gap-3">
            <input
              value={reranker.provider}
              onChange={(e) => markDirty(() => setReranker({ ...reranker, provider: e.target.value }))}
              placeholder={t("memory.providerPlaceholder") || "jina"}
              className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm"
            />
            <input
              value={reranker.model}
              onChange={(e) => markDirty(() => setReranker({ ...reranker, model: e.target.value }))}
              placeholder={t("memory.rerankerModelPlaceholder") || "jina-reranker-v2-base-multilingual"}
              className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm"
            />
            <input
              type="password"
              value={reranker.apiKey}
              onChange={(e) => markDirty(() => setReranker({ ...reranker, apiKey: e.target.value }))}
              placeholder={t("memory.apiKeyPlaceholder") || "API key"}
              className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm"
            />
            <input
              value={reranker.apiBase}
              onChange={(e) => markDirty(() => setReranker({ ...reranker, apiBase: e.target.value }))}
              placeholder={t("memory.rerankerApiBasePlaceholder") || "https://api.jina.ai/v1"}
              className="rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm"
            />
          </div>
        )}
      </section>

      {/* Settings */}
      <section className="mb-8 rounded-lg border border-zinc-800 bg-zinc-900 p-6">
        <h2 className="mb-1 font-semibold">{t("memory.memorySettings") || "Memory Settings"}</h2>
        <p className="mb-4 text-sm text-zinc-500">
          {t("memory.settingsDesc") || "Enable cross-session memory recall for agents."}
        </p>
        <label className="mb-4 flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={settings.enabled}
            onChange={(e) => markDirty(() => setSettings({ enabled: e.target.checked }))}
            className="rounded"
          />
          {t("memory.enabled") || "Enabled"}
        </label>
      </section>

      {dirty && (
        <button
          onClick={handleSave}
          disabled={saving}
          className="rounded bg-primary px-4 py-2 text-sm text-primary-foreground"
        >
          {saving ? (t("common.saving") || "Saving...") : (t("common.save") || "Save")}
        </button>
      )}
    </div>
  );
}
