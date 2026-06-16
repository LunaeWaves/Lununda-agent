"use client";

import * as React from "react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "@/components/ui/sidebar";
import {
  ChevronsUpDownIcon,
  LogOutIcon,
  MoonIcon,
  SunIcon,
} from "lucide-react";
import { useTheme } from "@/components/theme-provider";
import { logout as doLogout } from "@/lib/auth";
import { useT } from "@/lib/i18n";

export function NavUser({
  name = "Admin",
  subtitle = "Gateway running",
  avatarUrl,
}: {
  name?: string;
  subtitle?: string;
  // avatarUrl, when set, overrides the default /user.png avatar.
  // Comes from me.user.avatarUrl; backend persists a path under
  // /api/users/{id}/files/avatar.png when the user uploads one.
  avatarUrl?: string;
}) {
  const { isMobile } = useSidebar();
  const t = useT();
  const { resolvedTheme, toggleTheme } = useTheme();

  // Avatar: explicit avatarUrl (user-uploaded) wins, otherwise /user.png
  // (the platform logo). onError falls back to /user.png too so a stale
  // avatar URL (deleted object, store clobber) doesn't render a broken
  // image.
  const [avatarSrc, setAvatarSrc] = React.useState(
    avatarUrl && avatarUrl.length > 0 ? avatarUrl : "/user.png",
  );
  React.useEffect(() => {
    setAvatarSrc(avatarUrl && avatarUrl.length > 0 ? avatarUrl : "/user.png");
  }, [avatarUrl]);
  const onAvatarError = () => {
    if (avatarSrc !== "/user.png") setAvatarSrc("/user.png");
  };

  const renderAvatar = (sizeClass: string) => (
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={avatarSrc}
      alt=""
      className={`aspect-square ${sizeClass} rounded-lg object-cover`}
      onError={onAvatarError}
    />
  );

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <SidebarMenuButton
                size="lg"
                className="data-open:bg-sidebar-accent data-open:text-sidebar-accent-foreground"
              />
            }
          >
            {renderAvatar("size-8")}
            <div className="grid flex-1 text-left text-sm leading-tight">
              <span className="truncate font-medium">{name}</span>
              <span className="truncate text-xs text-muted-foreground">
                {subtitle}
              </span>
            </div>
            <ChevronsUpDownIcon className="ml-auto size-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="min-w-56 rounded-lg"
            side={isMobile ? "bottom" : "right"}
            align="end"
            sideOffset={4}
          >
            <DropdownMenuGroup>
              <DropdownMenuLabel className="p-0 font-normal">
                <div className="flex items-center gap-2 px-1 py-1.5 text-left text-sm">
                  {renderAvatar("size-8")}
                  <div className="grid flex-1 text-left text-sm leading-tight">
                    <span className="truncate font-medium">{name}</span>
                    <span className="truncate text-xs text-muted-foreground">
                      {subtitle}
                    </span>
                  </div>
                </div>
              </DropdownMenuLabel>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={(e) => {
                e.preventDefault();
                toggleTheme();
              }}
            >
              {resolvedTheme === "dark" ? <SunIcon /> : <MoonIcon />}
              <span>{t(resolvedTheme === "dark" ? "user.lightMode" : "user.darkMode")}</span>
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={() => {
                doLogout();
                window.location.href = "/";
              }}
            >
              <LogOutIcon />
              <span>{t("user.logout")}</span>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
