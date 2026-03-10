import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // output: 'export' is set for production S3 deployment
  // Comment out for local dev with dynamic routes
  ...(process.env.NODE_ENV === 'production' ? { output: 'export' as const } : {}),
  images: {
    unoptimized: true,
  },
};

export default nextConfig;
