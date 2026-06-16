"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { register, getStatus } from "@/lib/api";
import { useT } from "@/lib/i18n";

export default function SignupPage() {
  const router = useRouter();
  const t = useT();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState<boolean | null>(null);

  useEffect(() => {
    let aborted = false;
    getStatus()
      .then((s) => { if (!aborted) setOpen(!!s.registrationOpen); })
      .catch(() => { if (!aborted) setOpen(false); });
    return () => { aborted = true; };
  }, []);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (!username.trim() || !email.trim() || !password) {
      setError(t("signup.allFieldsRequired"));
      return;
    }
    if (password.length < 8) {
      setError(t("signup.passwordMin"));
      return;
    }
    if (password !== confirm) {
      setError(t("signup.passwordMismatch"));
      return;
    }
    setLoading(true);
    try {
      const res = await register({ username: username.trim(), email: email.trim(), password });
      if (!res.ok) {
        setError(res.error || t("signup.createFailed"));
        setLoading(false);
        return;
      }
      router.replace("/overview/");
    } catch {
      setError(t("login.cannotReach"));
      setLoading(false);
    }
  }

  if (open === null) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-zinc-950">
        <div className="h-8 w-8 animate-spin rounded-full border-2 border-zinc-700 border-t-primary" />
      </div>
    );
  }

  if (!open) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-zinc-950 p-4">
        <div className="w-full max-w-sm space-y-4 text-center">
          <h1 className="text-2xl font-bold text-zinc-100">{t("signup.registrationClosed")}</h1>
          <p className="text-sm text-zinc-500">
            {t("signup.registrationClosedDesc")}
          </p>
          <Link
            href="/"
            className="inline-block rounded-lg bg-primary text-primary-foreground px-4 py-2 text-sm font-medium text-white hover:bg-primary/90"
          >
            {t("signup.backToSignIn")}
          </Link>
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-zinc-950 p-4">
      <div className="w-full max-w-sm space-y-6">
        <div className="text-center space-y-2">
          <h1 className="text-2xl font-bold text-zinc-100">{t("signup.title")}</h1>
          <p className="text-sm text-zinc-500">{t("signup.subtitle")}</p>
        </div>
        <form onSubmit={handleSubmit} className="space-y-4">
          <input
            type="text"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder={t("signup.usernamePlaceholder")}
            autoFocus
            autoComplete="username"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-primary focus:ring-1 focus:ring-primary"
          />
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder={t("signup.emailPlaceholder")}
            autoComplete="email"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-primary focus:ring-1 focus:ring-primary"
          />
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t("signup.passwordPlaceholder")}
            autoComplete="new-password"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-primary focus:ring-1 focus:ring-primary"
          />
          <input
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            placeholder={t("signup.confirmPlaceholder")}
            autoComplete="new-password"
            className="w-full rounded-lg border border-zinc-800 bg-zinc-900 px-4 py-3 text-sm text-zinc-100 placeholder-zinc-600 outline-none focus:border-primary focus:ring-1 focus:ring-primary"
          />
          {error && <p className="text-sm text-destructive">{error}</p>}
          <button
            type="submit"
            disabled={loading || !username.trim() || !email.trim() || !password || !confirm}
            className="w-full rounded-lg bg-primary text-primary-foreground px-4 py-3 text-sm font-medium text-white transition hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {loading ? t("signup.submitting") : t("signup.submit")}
          </button>
        </form>
        <p className="text-center text-sm text-zinc-500">
          {t("signup.hasAccount")}{" "}
          <Link href="/" className="text-primary hover:text-primary/80">
            {t("signup.signIn")}
          </Link>
        </p>
      </div>
    </div>
  );
}
