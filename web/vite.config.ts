import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

// dist/ is embedded by the Go binary and ignored by Git except for .gitkeep,
// which the build must put back after emptying the directory.
function keepGitkeep(): Plugin {
  return {
    name: "mybuilds-gitkeep",
    closeBundle() {
      writeFileSync(fileURLToPath(new URL("dist/.gitkeep", import.meta.url)), "");
    },
  };
}

export default defineConfig({
  plugins: [react(), keepGitkeep()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    port: 5173,
    // The Go server owns the API; `go run ./cmd/server` on :8080 serves it.
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/hooks": "http://127.0.0.1:8080",
      "/healthz": "http://127.0.0.1:8080",
      "/readyz": "http://127.0.0.1:8080",
    },
  },
});
