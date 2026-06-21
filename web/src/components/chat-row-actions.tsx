"use client";
import { useT } from "@/lib/i18n";

import * as React from "react";
import { useRouter } from "next/navigation";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useSidebar } from "@/components/ui/sidebar";
import { Check, Copy, Link2Icon, MoreHorizontalIcon, PencilIcon, Trash2Icon } from "lucide-react";
import {
  createSessionShare,
  deleteChatSession,
  getActiveSessionShare,
  renameChatSession,
  revokeSessionShare,
} from "@/lib/api";

// ChatRowActions is the shared "..." dropdown attached to every chat
// row in the sidebar — both the flat "Chats" list and the chats nested
// under a project. It owns its own Edit / Delete dialog state so the
// caller only needs to render the trigger and the dialogs unmount the
// instant they close.
//
// Variant controls absolute positioning + hover gating:
//   "menu-item"     — paired with SidebarMenuButton (top-1.5 right-1)
//   "menu-sub-item" — paired with SidebarMenuSubButton (smaller; uses
//                     the group/menu-sub-item hover scope so the trigger
//                     only fades in when the sub-row is hovered).

export interface ChatRowSession {
  id: string;
  title: string;
}

export function ChatRowActions({
  agentId,
  session,
  onChanged,
  variant = "menu-item",
}: {
  agentId: string;
  session: ChatRowSession;
  onChanged: () => void;
  variant?: "menu-item" | "menu-sub-item";
}) {
  const t = useT();
  const router = useRouter();
  const { isMobile } = useSidebar();
  const [editOpen, setEditOpen] = React.useState(false);
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const [shareOpen, setShareOpen] = React.useState(false);

  const onConfirmDelete = async () => {
    setDeleteOpen(false);
    try {
      await deleteChatSession(agentId, session.id);
    } finally {
      // If the deleted session is currently open, bounce back to the
      // fresh chat URL so the page doesn't hang on a stale id.
      if (
        typeof window !== "undefined" &&
        window.location.pathname.replace(/\/$/, "").endsWith("/chat/" + session.id)
      ) {
        router.replace(`/agents/${encodeURIComponent(agentId)}/chat/`);
      }
      onChanged();
    }
  };

  // Trigger styling: SidebarMenuAction (used by the flat chats list)
  // hooks into group-hover/menu-item; project sub-rows use a different
  // group selector (group/menu-sub-item) and a smaller chip so it fits
  // the h-7 sub-button. The two trigger flavors live here so callers
  // don't have to know either layout's details.
  //
  // The sub-item variant uses right:-20px so the chip pokes out past
  // the SidebarMenuSub's mx-3.5 (14px margin) PLUS px-2.5 (10px
  // padding) inset and ends up flush with the parent project row's
  // `...` action (which sits at right:4px). 14 + 10 - 4 = 20 →
  // right:-20px. Without this escape the sub chip sits ~20px to the
  // left of the parent's chip.
  const triggerClass =
    variant === "menu-sub-item"
      ? "absolute top-1 right-[-20px] flex h-5 w-5 items-center justify-center rounded-md text-sidebar-foreground outline-hidden transition-opacity hover:bg-sidebar-accent hover:text-sidebar-accent-foreground aria-expanded:opacity-100 md:opacity-0 group-hover/menu-sub-item:opacity-100 group-focus-within/menu-sub-item:opacity-100 [&>svg]:size-4"
      : "absolute top-1.5 right-1 flex aspect-square w-5 items-center justify-center rounded-md p-0 text-sidebar-foreground outline-hidden transition-transform hover:bg-sidebar-accent hover:text-sidebar-accent-foreground aria-expanded:opacity-100 md:opacity-0 group-hover/menu-item:opacity-100 group-focus-within/menu-item:opacity-100 [&>svg]:size-4";

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <button type="button" className={triggerClass}>
              <MoreHorizontalIcon />
              <span className="sr-only">Chat actions</span>
            </button>
          }
        />
        <DropdownMenuContent
          className="w-40 rounded-lg"
          side={isMobile ? "bottom" : "right"}
          align={isMobile ? "end" : "start"}
        >
          <DropdownMenuItem onClick={() => setEditOpen(true)}>
            <PencilIcon className="text-muted-foreground" />
            <span>{t("sidebar.editProject")}</span>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={() => setShareOpen(true)}>
            <Link2Icon className="text-muted-foreground" />
            <span>{t("sidebar.shareReadOnly")}</span>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            onClick={() => setDeleteOpen(true)}
            className="text-destructive focus:text-destructive"
          >
            <Trash2Icon className="text-destructive" />
            <span>{t("sidebar.deleteChat")}</span>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <EditTitleDialog
        open={editOpen}
        onOpenChange={setEditOpen}
        agentId={agentId}
        session={session}
        onSaved={onChanged}
      />

      <ShareReadOnlyDialog
        open={shareOpen}
        onOpenChange={setShareOpen}
        agentId={agentId}
        sessionId={session.id}
        sessionTitle={session.title}
      />

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("sidebar.deleteChat")}</AlertDialogTitle>
            <AlertDialogDescription>
              Delete <strong>{session.title || session.id}</strong>? The full
              message history for this chat will be removed and cannot be
              recovered.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("sidebar.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={onConfirmDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function EditTitleDialog({
  open,
  onOpenChange,
  agentId,
  session,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  agentId: string;
  session: ChatRowSession;
  onSaved: () => void;
}) {
  const t = useT();
  const [draft, setDraft] = React.useState("");
  const [saving, setSaving] = React.useState(false);

  // Re-prime the draft each time the dialog opens. Without this, a
  // user who edits, cancels, and re-opens would see their stale draft.
  React.useEffect(() => {
    if (open) setDraft(session.title ?? "");
  }, [open, session.title]);

  const save = async () => {
    const next = draft.trim();
    if (!next || next === session.title) {
      onOpenChange(false);
      return;
    }
    setSaving(true);
    try {
      await renameChatSession(agentId, session.id, next);
      onSaved();
    } finally {
      setSaving(false);
      onOpenChange(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("sidebar.editTitle")}</DialogTitle>
          <DialogDescription>
            Rename this chat so it&apos;s easier to find in the sidebar.
          </DialogDescription>
        </DialogHeader>
        <Input
          autoFocus
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            // Ignore Enter while a CJK IME composition is active —
            // otherwise selecting a candidate would submit the dialog
            // prematurely.
            if (e.nativeEvent.isComposing || e.keyCode === 229) return;
            if (e.key === "Enter") {
              e.preventDefault();
              save();
            }
          }}
          placeholder={t("sidebar.chatTitle")}
        />
        <DialogFooter>
          <Button
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            Cancel
          </Button>
          <Button onClick={save} disabled={saving || !draft.trim()}>
            {saving ? t("sidebar.saving") : t("sidebar.saveProject")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ShareReadOnlyDialog generates and displays a read-only share link for
// the session (agent privatization D3). One active share per session —
// re-opening the dialog after generation shows the existing link with a
// Revoke button. Owner-only; server enforces ownership too.
function ShareReadOnlyDialog({
  open,
  onOpenChange,
  agentId,
  sessionId,
  sessionTitle,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  agentId: string;
  sessionId: string;
  sessionTitle: string;
}) {
  const t = useT();
  const [share, setShare] = React.useState<{ token: string; url: string } | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [loadingExisting, setLoadingExisting] = React.useState(false);
  const [revoked, setRevoked] = React.useState(false);
  const [copied, setCopied] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  // On open: fetch the existing active share (if any) so the owner sees
  // the current link first, not a "generate" button that mints a new
  // one and revokes the old silently.
  React.useEffect(() => {
    if (!open) return;
    setShare(null);
    setRevoked(false);
    setCopied(false);
    setError(null);
    setLoadingExisting(true);
    getActiveSessionShare(agentId, sessionId)
      .then((s) => {
        if (s) setShare({ token: s.token, url: s.url });
      })
      .catch(() => {})
      .finally(() => setLoadingExisting(false));
  }, [open, agentId, sessionId]);

  const generate = async () => {
    setBusy(true);
    setError(null);
    try {
      const s = await createSessionShare(agentId, sessionId);
      setShare({ token: s.token, url: s.url });
      setRevoked(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : "share failed");
    } finally {
      setBusy(false);
    }
  };

  const revoke = async () => {
    if (!share) return;
    setBusy(true);
    setError(null);
    try {
      await revokeSessionShare(agentId, sessionId);
      setShare(null);
      setRevoked(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : "revoke failed");
    } finally {
      setBusy(false);
    }
  };

  const copyLink = async () => {
    if (!share || typeof window === "undefined") return;
    const full = `${window.location.origin}${share.url}`;
    try {
      await navigator.clipboard.writeText(full);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // clipboard blocked — user can still select the input
    }
  };

  const fullUrl =
    share && typeof window !== "undefined"
      ? `${window.location.origin}${share.url}`
      : "";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t("sidebar.shareReadOnly")}</DialogTitle>
          <DialogDescription>
            {t("sidebar.shareReadOnlyDesc", { title: sessionTitle || sessionId })}
          </DialogDescription>
        </DialogHeader>

        {!share && !revoked && (
          <Button onClick={generate} disabled={busy || loadingExisting}>
            {loadingExisting
              ? "…"
              : busy
                ? t("sidebar.saving")
                : t("sidebar.generateShareLink")}
          </Button>
        )}

        {share && (
          <div className="space-y-2">
            <Input readOnly value={fullUrl} onFocus={(e) => e.currentTarget.select()} className="font-mono text-xs" />
            <div className="flex gap-2">
              <Button variant="outline" onClick={copyLink} disabled={busy}>
                {copied ? (
                  <>
                    <Check className="h-4 w-4 mr-1.5" /> {t("common.copied")}
                  </>
                ) : (
                  <>
                    <Copy className="h-4 w-4 mr-1.5" /> {t("common.copy")}
                  </>
                )}
              </Button>
              <Button variant="outline" onClick={generate} disabled={busy}>
                {t("sidebar.regenerate")}
              </Button>
              <Button variant="destructive" onClick={revoke} disabled={busy}>
                {t("sidebar.revoke")}
              </Button>
            </div>
          </div>
        )}

        {revoked && (
          <p className="text-sm text-muted-foreground">{t("sidebar.shareRevoked")}</p>
        )}

        {error && <p className="text-sm text-destructive">{error}</p>}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
