// 构建配置：产物输出到 dist/，由 framework/web/embed.go 嵌入桌面端（以后 api-server 同样嵌入）。
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  base: "./",
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 1024,
  },
});
