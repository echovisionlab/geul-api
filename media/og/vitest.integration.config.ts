import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    pool: "forks",
    fileParallelism: false,
    isolate: true,
    maxWorkers: 1,
    include: ["src/**/*.integration.test.{ts,tsx}"],
    coverage: {
      enabled: false,
    },
  },
});
