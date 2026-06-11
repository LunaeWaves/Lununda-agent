"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
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
  { type: "query", label: "查询", icon: SearchIcon },
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
  const [graphHtml, setGraphHtml] = useState<string>("");

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

  const handleGenerate = useCallback(async () => {
    if (!agentId || kbSources.length === 0) return;
    setGenerating(true);
    try {
      await generateWiki(
        agentId,
        kbSources.map((s) => s.id),
      );
      // Wait a moment then refresh
      setTimeout(() => {
        loadData();
        setGenerating(false);
      }, 3000);
    } catch {
      setGenerating(false);
    }
  }, [agentId, kbSources, loadData]);

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
    try {
      const g = await getWikiGraph(agentId);
      const svg = renderGraphSVG(g.nodes, g.edges);
      setGraphHtml(svg);
      setShowGraph(true);
    } catch {}
  }, [agentId]);

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
      <div className="w-64 shrink-0 border-r bg-zinc-950/50 flex flex-col">
        <div className="p-3 border-b">
          <div className="flex items-center justify-between">
            <h3 className="text-sm font-semibold">Wiki</h3>
            <div className="flex gap-1">
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7"
                onClick={handleGenerate}
                disabled={generating || kbSources.length === 0}
                title="生成 Wiki"
              >
                <SparklesIcon className="h-4 w-4" />
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
            <div
              className="flex-1 p-4"
              dangerouslySetInnerHTML={{ __html: graphHtml }}
            />
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
                <Button onClick={handleGenerate} disabled={generating}>
                  <SparklesIcon className="h-4 w-4 mr-2" />
                  {generating ? "生成中..." : "生成 Wiki"}
                </Button>
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

// Simple SVG graph renderer (no d3 dependency)
function renderGraphSVG(
  nodes: WikiPage[],
  edges: { src_page_id: string; dst_page_id: string; relation: string }[],
): string {
  if (nodes.length === 0) {
    return '<p class="text-center text-muted-foreground mt-20">暂无图谱数据</p>';
  }

  const W = 800;
  const H = 600;
  const cx = W / 2;
  const cy = H / 2;

  // Position nodes in a circle
  const positions: Record<string, { x: number; y: number }> = {};
  const R = Math.min(W, H) * 0.35;
  nodes.forEach((n, i) => {
    const angle = (2 * Math.PI * i) / nodes.length - Math.PI / 2;
    positions[n.id] = {
      x: cx + R * Math.cos(angle),
      y: cy + R * Math.sin(angle),
    };
  });

  // Type colors
  const typeColors: Record<string, string> = {
    overview: "#8b5cf6",
    entity: "#3b82f6",
    concept: "#10b981",
    source: "#f59e0b",
    query: "#ef4444",
  };

  let svg = `<svg viewBox="0 0 ${W} ${H}" class="w-full h-full">`;

  // Edges
  for (const e of edges) {
    const s = positions[e.src_page_id];
    const d = positions[e.dst_page_id];
    if (!s || !d) continue;
    svg += `<line x1="${s.x}" y1="${s.y}" x2="${d.x}" y2="${d.y}" stroke="#444" stroke-width="1" opacity="0.5"/>`;
  }

  // Nodes
  for (const n of nodes) {
    const pos = positions[n.id];
    if (!pos) continue;
    const color = typeColors[n.page_type] || "#666";
    const label = n.title.length > 10 ? n.title.slice(0, 10) + "…" : n.title;
    svg += `<circle cx="${pos.x}" cy="${pos.y}" r="18" fill="${color}" opacity="0.8"/>`;
    svg += `<text x="${pos.x}" y="${pos.y + 32}" text-anchor="middle" fill="#ccc" font-size="10">${label}</text>`;
  }

  svg += "</svg>";
  return svg;
}
