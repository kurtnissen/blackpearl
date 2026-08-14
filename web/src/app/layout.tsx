import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "BlackPearl Setup",
  description: "Local TorBox and Plex stream setup",
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>): React.JSX.Element {
  return <html lang="en"><body>{children}</body></html>;
}
