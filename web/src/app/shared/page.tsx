"use client";

import * as React from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { ChatMarkdown } from "@/components/chat-markdown";
import { getSharedSession, type SharedMessage } from "@/lib/api";
import { localizeSlashReply } from "@/lib/slash-reply";
import { useT } from "@/lib/i18n";
import { Sparkles } from "lucide-react";

// /shared?token=X is the public read-only view of a session share.
// The Go /share/{token} handler validates the token and 302s here so the
// URL can stay pretty (no query string) for sharing. This page fetches
// /api/share/{token} (public) and renders messages with ChatMarkdown —
// same primitive as the live chat screen — so code blocks, lists,
// tables, math, mermaid all render identically to chat.
export default function SharedSessionPage() {
  const params = useSearchParams();
  const token = params.get("token") || "";
  const t = useT();
  const [msgs, setMsgs] = React.useState<SharedMessage[] | null>(null);
  const [err, setErr] = React.useState<string | null>(null);

  React.useEffect(() => {
    if (!token) {
      setErr("missing token");
      return;
    }
    getSharedSession(token)
      .then((s) => setMsgs(s.messages || []))
      .catch((e) => setErr(e instanceof Error ? e.message : "load failed"));
  }, [token]);

  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="border-b bg-card/50 backdrop-blur">
        <div className="max-w-3xl mx-auto px-4 py-3 flex items-center gap-2">
          <Sparkles className="h-4 w-4 text-primary" />
          <span className="font-medium">Shared session</span>
          <span className="ml-auto text-xs text-muted-foreground">read-only</span>
        </div>
      </header>

      <main className="max-w-3xl mx-auto px-4 py-6 space-y-5">
        {err && (
          <div className="rounded-lg border border-destructive/30 bg-destructive/5 p-4 text-sm">
            {err}
          </div>
        )}
        {msgs === null && !err && (
          <div className="flex items-center justify-center py-16">
            <div className="h-8 w-8 animate-spin rounded-full border-2 border-muted border-t-primary" />
          </div>
        )}
        {msgs?.map((m, i) => (
          <MessageBubble key={i} msg={m} t={t} />
        ))}
        {msgs && msgs.length === 0 && (
          <p className="text-sm text-muted-foreground text-center py-12">
            (empty session)
          </p>
        )}
        <footer className="pt-4 text-center text-xs text-muted-foreground">
          Powered by{" "}
          <Link href="/" className="underline hover:text-foreground">
            Lununda Agent
          </Link>
        </footer>
      </main>
    </div>
  );
}

function MessageBubble({
  msg,
  t,
}: {
  msg: SharedMessage;
  t: (key: string, vars?: Record<string, string | number>) => string;
}) {
  const isUser = msg.role === "user";
  // Expand __SLASH:<code>:<args>__ sentinels the same way the live chat
  // does — via the i18n t() function. Non-sentinel content passes through.
  const content = isUser ? msg.content : localizeSlashReply(msg.content, t);
  return (
    <div className={`flex ${isUser ? "justify-end" : "justify-start"}`}>
      <div
        className={
          isUser
            ? "max-w-[85%] rounded-2xl bg-primary text-primary-foreground px-4 py-2.5"
            : "max-w-[85%] rounded-2xl bg-muted px-4 py-2.5"
        }
      >
        {!isUser && (
          <div className="mb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            assistant
          </div>
        )}
        <div className={isUser ? "text-[15px] leading-relaxed whitespace-pre-wrap break-words" : ""}>
          {isUser ? (
            content
          ) : (
            <ChatMarkdown text={content} />
          )}
        </div>
      </div>
    </div>
  );
}
