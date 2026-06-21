"use client";

import { useState, useEffect } from "react";
import { useRouter, usePathname } from "next/navigation";
import { getMe } from "@/lib/api";
import { LoginScreen } from "./login-screen";

interface AuthGuardProps {
  children: React.ReactNode;
}

// Routes that require an admin (super_admin) role. Server APIs enforce this
// authoritatively; the client gate just stops non-admins from landing on a
// page that would render an empty / 403'd shell.
//
// /settings, /models, and /apikeys are intentionally NOT here —
// settings hides Runtime; models merges system+user with badges;
// apikeys lets non-admins issue type=user/agent (only type=admin
// requires super_admin and that gate lives inside the create handler).
const ADMIN_PATH_PREFIXES = [
  "/memory",
  "/admin/",
  "/skills",
  "/providers",
  "/channels",
  "/channels-config",
  "/plugins",
  // "/tools" was here — removed so regular users can manage their own
  // tool provider keys (image-gen / web-search / TTS). The backend
  // /api/tools endpoint is already user-scope aware via loadUserConfig
  // + saveUserConfig; the only thing keeping users out was this gate.
  "/cron",
];

function isAdminPath(pathname: string): boolean {
  return ADMIN_PATH_PREFIXES.some(
    (p) => pathname === p || pathname === p.replace(/\/$/, "") || pathname.startsWith(p + (p.endsWith("/") ? "" : "/")),
  );
}

export function AuthGuard({ children }: AuthGuardProps) {
  const router = useRouter();
  const pathname = usePathname();
  const [checked, setChecked] = useState(false);
  const [authed, setAuthed] = useState(false);

  useEffect(() => {
    let aborted = false;
    (async () => {
      // Decide between three states:
      //   - users table empty → /onboard
      //   - users exist, caller has a session → render children
      //   - users exist, caller has no session → show LoginScreen
      let configured = false;
      try {
        const res = await fetch("/api/status", { credentials: "same-origin" });
        if (res.ok) {
          const status = await res.json();
          configured = !!status.configured;
        }
      } catch {
        // server down — fall through to LoginScreen
      }
      if (aborted) return;

      if (!configured) {
        const onOnboard = pathname === "/onboard" || pathname.startsWith("/onboard/");
        if (!onOnboard) {
          router.replace("/onboard/");
          return;
        }
        setAuthed(true);
        setChecked(true);
        return;
      }

      // /signup is a public route when admin opens registration. Let it
      // render unauthenticated — the page itself re-checks the toggle and
      // surfaces "registration is closed" if the admin flipped it off
      // between page load and submit.
      if (pathname === "/signup" || pathname.startsWith("/signup/")) {
        setAuthed(true);
        setChecked(true);
        return;
      }

      // /shared is the public read-only session view (agent privatization
      // D3). Reachable without auth — the share token in the URL is the
      // only credential.
      if (pathname === "/shared" || pathname.startsWith("/shared/")) {
        setAuthed(true);
        setChecked(true);
        return;
      }

      try {
        const me = await getMe();
        if (me.ok && me.user) {
          if (isAdminPath(pathname) && me.user.role !== "super_admin") {
            router.replace("/overview/");
            return;
          }
          setAuthed(true);
        }
      } catch {
        // network failure — fall through to LoginScreen
      }
      if (!aborted) setChecked(true);
    })();
    return () => { aborted = true; };
  }, [router, pathname]);

  if (!checked) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <div className="h-8 w-8 animate-spin rounded-full border-2 border-muted border-t-primary" />
      </div>
    );
  }
  if (!authed) {
    return <LoginScreen onSuccess={() => setAuthed(true)} />;
  }
  return <>{children}</>;
}
