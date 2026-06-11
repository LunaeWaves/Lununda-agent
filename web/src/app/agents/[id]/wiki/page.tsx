"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Separator } from "@/components/ui/separator";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  BookOpenIcon,
  BrainIcon,
  ChevronRightIcon,
  DatabaseIcon,
  EyeIcon,
  FlaskConicalIcon,
  LightbulbIcon,
  NetworkIcon,
  RefreshCwIcon,
  SearchIcon,
  SparklesIcon,
  TrashIcon,
} from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import remarkBreaks from "remark-breaks";
import { ExternalAnchor } from "@/components/markdown-link";
import {
  type WikiPage,
  type WikiStats,
  getWikiStats,
  listWikiPages,
  getWikiPage,
  deleteWikiPage,
  generateWiki,
  getWikiGraph,
  listKBSources,
  type KBSource,
} from "@/lib/api";
import { useAgentIdFromURL } from "@/hooks/use-agent-id";
import { useAgentName } from "@/hooks/use-agent-name";
import { cn } from "@/lib/utils";

const PAGE_TYPE_SECTIONS = [
  { type: "overview", label: "总览", icon: EyeIcon },
  { type: "entity", label: "实体", icon: DatabaseIcon },
  { type: "concept", label: "概念", icon: LightbulbIcon },
  { type: "source", label: "来源", icon: BookOpenIcon },
];

export default function WikiPage() {
  const agentId = useAgentIdFromURL();
  const agentName = useAgentName(agentId);

  const [stats, setStats] = useState<WikiStats | null>(null);
  const [pages, setPages] = useState<WikiPage[]>([]);
  const [selectedPageId, setSelectedPageId] = useState<string | null>(null);
  const [selectedPage, setSelectedPage] = useState<WikiPage | null>(null);
  const [loading, setLoading] = useState(true);
  const [generating, setGenerating] = useState(false);
  const [filterType, setFilterType] = useState<string>("all");
  const [showGraph, setShowGraph] = useState(false);
  const graphRef = useRef<HTMLDivElement>(null);

  // KB sources for generation
  const [kbSources, setKbSources] = useState<KBSource[]>([]);

  const loadData = useCallback(async () => {
    if (!agentId) return;
    setLoading(true);
    try {
      const [s, p] = await Promise.all([
        getWikiStats(agentId),
        listWikiPages(agentId, filterType === "all" ? undefined : filterType),
      ]);
      setStats(s);
      setPages(p.pages ?? []);
    } catch {}
    setLoading(false);
  }, [agentId, filterType]);

  useEffect(() => {
    loadData();
  }, [loadData]);

  // Load KB sources for generation UI
  useEffect(() => {
    if (!agentId) return;
    listKBSources(agentId).then(setKbSources).catch(() => {});
  }, [agentId]);

  const handleSelectPage = useCallback(
    async (pageId: string) => {
      if (!agentId) return;
      setSelectedPageId(pageId);
      try {
        const p = await getWikiPage(agentId, pageId);
        setSelectedPage(p);
      } catch {}
    },
    [agentId],
  );

  const handleGenerate = useCallback(async (force?: boolean) => {
    if (!agentId || kbSources.length === 0) return;
    setGenerating(true);
    try {
      const res = await generateWiki(
        agentId,
        kbSources.map((s) => s.id),
        force,
      );
      if (res.status === "already_running") {
        alert("Wiki 生成正在进行中，请等待完成后再试。");
        setGenerating(false);
        return;
      }
      setTimeout(() => {
        loadData();
        setGenerating(false);
      }, 3000);
    } catch {
      setGenerating(false);
    }
  }, [agentId, kbSources, loadData]);

  const handleForceGenerate = useCallback(() => {
    if (!window.confirm("强制重新生成将删除已有 Wiki 页面并重新分析所有知识库源，确定继续？")) return;
    handleGenerate(true);
  }, [handleGenerate]);

  const unprocessedCount = kbSources.filter((s) => !s.wiki_generated_at).length;

  const handleDelete = useCallback(
    async (pageId: string) => {
      if (!agentId) return;
      try {
        await deleteWikiPage(agentId, pageId);
        if (selectedPageId === pageId) {
          setSelectedPageId(null);
          setSelectedPage(null);
        }
        loadData();
      } catch {}
    },
    [agentId, selectedPageId, loadData],
  );

  const handleLoadGraph = useCallback(async () => {
    if (!agentId) return;
    setShowGraph(true);
  }, [agentId]);

  // Render vis-network graph
  useEffect(() => {
    if (!showGraph || !graphRef.current || !agentId) return;
    let network: import("vis-network").Network | null = null;
    let cancelled = false;

    const init = async () => {
      const [{ Network }, { DataSet }] = await Promise.all([
        import("vis-network/standalone"),
        import("vis-data/standalone"),
      ]);
      if (cancelled) return;

      const g = await getWikiGraph(agentId);
      if (cancelled) return;

      const typeColors: Record<string, string> = {
        overview: "#8b5cf6",
        entity: "#3b82f6",
        concept: "#10b981",
        source: "#f59e0b",
        query: "#ef4444",
      };

      const nodes = new DataSet(
        g.nodes.map((n) => ({
          id: n.id,
          label: n.title.length > 12 ? n.title.slice(0, 12) + "…" : n.title,
          title: n.title,
          color: { background: typeColors[n.page_type] || "#666", border: "#333" },
          font: { size: 11, color: "#e5e7eb" },
          shape: "dot",
          size: 20,
        })),
      );

      const edges = new DataSet(
        g.edges.map((e, i) => ({
          id: i + 1,
          from: e.src_page_id,
          to: e.dst_page_id,
          title: e.relation,
          arrows: "to",
          color: { color: "#555", opacity: 0.4 },
          width: 1,
        })),
      );

      network = new Network(graphRef.current!, { nodes, edges }, {
        physics: { stabilization: { iterations: 100 }, solver: "forceAtlas2Based" },
        interaction: { hover: true, tooltipDelay: 200 },
        edges: { smooth: true },
      });
    };

    init();
    return () => { cancelled = true; if (network) network.destroy(); };
  }, [showGraph, agentId]);

  // Group pages by type
  const grouped = useMemo(() => {
    const m: Record<string, WikiPage[]> = {};
    for (const p of pages) {
      if (!m[p.page_type]) m[p.page_type] = [];
      m[p.page_type].push(p);
    }
    return m;
  }, [pages]);

  return (
    <div className="flex h-[calc(100vh-3.5rem)]">
      {/* Left: Tree navigation */}
      <div className="w-64 shrink-0 border-r bg-muted/30 flex flex-col">
        <div className="p-3 border-b">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold">Wiki</h3>
            <div className="flex gap-1">
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                onClick={() => handleGenerate()}
                disabled={generating || unprocessedCount === 0}
                title={unprocessedCount === 0 ? "所有源已处理" : "生成未处理的 Wiki"}
              >
                <SparklesIcon className="h-4 w-4" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                onClick={handleForceGenerate}
                disabled={generating || kbSources.length === 0}
                title="强制重新生成所有 Wiki"
              >
                <RefreshCwIcon className="h-3.5 w-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                onClick={handleLoadGraph}
                title="知识图谱"
              >
                <NetworkIcon className="h-4 w-4" />
              </Button>
            </div>
          </div>
          {stats && (
            <p className="text-xs text-muted-foreground mt-1">
              {stats.total_pages} 页 · {stats.total_edges} 链接
            </p>
          )}
        </div>

        <ScrollArea className="flex-1">
          <div className="p-2">
            {PAGE_TYPE_SECTIONS.map((section) => {
              const sectionPages = grouped[section.type] || [];
              return (
                <div key={section.type} className="mb-2">
                  <div className="flex items-center gap-1.5 px-2 py-1 text-xs font-medium text-muted-foreground">
                    <section.icon className="h-3 w-3" />
                    {section.label}
                    {sectionPages.length > 0 && (
                      <span className="ml-auto">{sectionPages.length}</span>
                    )}
                  </div>
                  {sectionPages.map((page) => (
                    <button
                      key={page.id}
                      className={cn(
                        "w-full text-left px-3 py-1.5 text-sm rounded-md hover:bg-accent flex items-center gap-1.5",
                        selectedPageId === page.id && "bg-accent font-medium",
                      )}
                      onClick={() => handleSelectPage(page.id)}
                    >
                      <ChevronRightIcon className="h-3 w-3 shrink-0 text-muted-foreground" />
                      <span className="truncate">{page.title}</span>
                    </button>
                  ))}
                </div>
              );
            })}
          </div>
        </ScrollArea>
      </div>

      {/* Center: Markdown preview */}
      <div className="flex-1 flex flex-col min-w-0">
        {showGraph ? (
          <div className="flex-1 flex flex-col">
            <div className="p-3 border-b flex items-center gap-2">
              <h3 className="text-sm font-semibold">知识图谱</h3>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setShowGraph(false)}
              >
                返回
              </Button>
            </div>
            <div ref={graphRef} className="flex-1" />
          </div>
        ) : selectedPage ? (
          <ScrollArea className="flex-1">
            <div className="p-6 max-w-4xl">
              <div className="flex items-center gap-2 mb-4">
                <Badge variant="outline">{selectedPage.page_type}</Badge>
                <h1 className="text-xl font-bold">{selectedPage.title}</h1>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 ml-auto"
                  onClick={() => handleDelete(selectedPage.id)}
                >
                  <TrashIcon className="h-4 w-4" />
                </Button>
              </div>
              <Separator className="mb-4" />
              <div className="prose prose-sm dark:prose-invert max-w-none">
                <ReactMarkdown
                  remarkPlugins={[remarkGfm, remarkBreaks]}
                  components={{
                    a: ExternalAnchor,
                    // Handle [[type:slug]] wiki links
                    p: ({ children }) => {
                      const text = String(children);
                      const parts = text.split(
                        /\[\[(\w+:[\w-]+)\]\]/g,
                      );
                      if (parts.length <= 1) return <p>{children}</p>;
                      return (
                        <p>
                          {parts.map((part, i) => {
                            if (i % 2 === 1) {
                              return (
                                <button
                                  key={i}
                                  className="text-primary underline hover:text-primary/80"
                                  onClick={() => handleSelectPage(part)}
                                >
                                  {part}
                                </button>
                              );
                            }
                            return <span key={i}>{part}</span>;
                          })}
                        </p>
                      );
                    },
                  }}
                >
                  {selectedPage.body || ""}
                </ReactMarkdown>
              </div>
            </div>
          </ScrollArea>
        ) : (
          <div className="flex-1 flex items-center justify-center text-muted-foreground">
            <div className="text-center">
              <BookOpenIcon className="h-12 w-12 mx-auto mb-4 opacity-50" />
              <h2 className="text-lg font-semibold mb-1">Wiki 知识图谱</h2>
              <p className="text-sm mb-4">
                {agentName
                  ? `${agentName} 的结构化知识库`
                  : "从知识库源生成结构化 Wiki 页面"}
              </p>
              {pages.length === 0 && kbSources.length > 0 && (
                <div className="space-y-2">
                  <Button onClick={() => handleGenerate()} disabled={generating || unprocessedCount === 0}>
                    <SparklesIcon className="h-4 w-4 mr-2" />
                    {generating ? "生成中..." : `生成 Wiki (${unprocessedCount} 待处理)`}
                  </Button>
                  {unprocessedCount < kbSources.length && (
                    <p className="text-xs text-muted-foreground">
                      {kbSources.length - unprocessedCount} 个源已处理，
                      <button
                        className="underline hover:text-foreground ml-1"
                        onClick={handleForceGenerate}
                      >
                        强制重新生成全部
                      </button>
                    </p>
                  )}
                </div>
              )}
              {kbSources.length === 0 && (
                <p className="text-xs">
                  请先在知识库管理中添加数据源
                </p>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

