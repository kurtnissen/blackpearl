import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "export",
  poweredByHeader: false,
  generateBuildId: async () => "blackpearl-setup",
};

export default nextConfig;
