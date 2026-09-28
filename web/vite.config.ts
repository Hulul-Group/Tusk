import path from "node:path"
import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"

// During `npm run dev` / `npm run preview`, API calls are proxied to a local Tusk on :3000.
const apiTarget = process.env.TUSK_API ?? "http://127.0.0.1:3000"
const proxy = {
  "/api": { target: apiTarget, changeOrigin: true },
  "/login/oidc": { target: apiTarget, changeOrigin: true },
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  server: { proxy },
  preview: { proxy },
  build: {
    target: "es2022",
    sourcemap: false,
    chunkSizeWarningLimit: 600,
  },
})
