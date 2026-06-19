// Slash command replies arrive as locale-agnostic sentinels
// (__SLASH:<code>:<json-args>__) emitted by the Go agent so the web UI
// can translate them. (IM channels expand them to English server-side;
// see internal/agent/slash_i18n.go.) This helper turns a sentinel into
// localized text via the i18n `t` function. Non-sentinel content is
// returned unchanged.
//
// Applied at assistant-message render time so it covers BOTH the live
// stream and persisted history (sentinels are locale-stable in the DB).

const SLASH_RE = /^__SLASH:([a-z_]+):([\s\S]*)__$/;

export function localizeSlashReply(
  content: string | undefined,
  t: (key: string, vars?: Record<string, string | number>) => string,
): string {
  if (!content || !content.includes("__SLASH:")) return content || "";
  const m = content.trim().match(SLASH_RE);
  if (!m) return content;
  let args: Record<string, string | number> = {};
  try {
    const raw = JSON.parse(m[2]);
    if (raw && typeof raw === "object") {
      for (const k of Object.keys(raw)) {
        const v = raw[k];
        args[k] = typeof v === "number" ? v : String(v);
      }
    }
  } catch {
    return content; // malformed sentinel — show raw rather than crash
  }
  return t("slash." + m[1], args);
}
