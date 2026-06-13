"use client";

import { useEffect, useState } from "react";
import { getMe } from "@/lib/api";
import { useT } from "@/lib/i18n";

export function ActAsBanner() {
  const [actAs, setActAs] = useState<string>("");
  const t = useT();

  useEffect(() => {
    let aborted = false;
    (async () => {
      const me = await getMe();
      if (aborted) return;
      if (me.actAsUserId) setActAs(me.actAsUserId);
    })();
    return () => { aborted = true; };
  }, []);

  if (!actAs) return null;
  return (
    <div className="sticky top-0 z-50 bg-amber-700/80 px-4 py-2 text-center text-xs text-amber-50 backdrop-blur">
      {t("banner.viewingAs")} <code className="font-mono">{actAs}</code> · {t("banner.readOnly")}
    </div>
  );
}
