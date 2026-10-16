import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "path";
import { readFileSync } from "fs";

const pkg = JSON.parse(
  readFileSync(path.resolve(__dirname, "package.json"), "utf-8"),
) as { version?: string };
const appVersion = pkg.version ?? "0.0.0";
const srcDir = path.resolve(__dirname, "src");

function atSrcAlias() {
  return {
    name: "nyro-at-src-alias",
    enforce: "pre" as const,
    resolveId(id: string) {
      if (id.startsWith("@/")) return path.resolve(srcDir, id.slice(2));
      return null;
    },
  };
}

export default defineConfig({
  plugins: [atSrcAlias(), react()],
  define: {
    "import.meta.env.VITE_APP_VERSION": JSON.stringify(appVersion),
  },
  resolve: {
    alias: {
      "@": srcDir,
    },
  },
  build: {
    outDir: "dist",
    rollupOptions: {
      output: {
        manualChunks: {
          "vendor-react": ["react", "react-dom", "react-router-dom"],
          "vendor-query": ["@tanstack/react-query"],
        },
      },
    },
  },
  server: {
    proxy: {
      "/api": {
        target: "http://127.0.0.1:19531",
        changeOrigin: true,
      },
    },
  },
});
