// Layout for /shared (public read-only session view). Sets referrer policy
// so the share token in the URL query can't leak via Referer if the
// transcript ever links out.
export const metadata = {
  title: "Shared session",
  referrer: "no-referrer",
};

export default function SharedLayout({ children }: { children: React.ReactNode }) {
  return <>{children}</>;
}
