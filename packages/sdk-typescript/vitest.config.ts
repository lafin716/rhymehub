import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// Test against shared sources directly so `vitest` does not depend on a prior build of @rhymehub/shared.
export default defineConfig({
  resolve: {
    alias: {
      "@rhymehub/shared": fileURLToPath(new URL("../shared/src/index.ts", import.meta.url)),
    },
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
