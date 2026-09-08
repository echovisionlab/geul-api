import { defineConfig } from "vitest/config";

function resolveMaxWorkers(envName: string, fallback: number): number {
  const value = process.env[envName];
  if (!value) {
    return fallback;
  }

  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

export default defineConfig({
  test: {
    environment: "node",
    pool: "forks",
    fileParallelism: true,
    isolate: true,
    maxWorkers: resolveMaxWorkers("VITEST_OG_MAX_WORKERS", 2),
    sequence: {
      concurrent: false,
    },
    include: ["src/**/*.test.{ts,tsx}"],
    exclude: [
      "dist/**",
      "node_modules/**",
      "src/**/*.integration.test.{ts,tsx}",
    ],
    coverage: {
      provider: "v8",
      reporter: ["text", "html", "lcov"],
      include: ["src/**/*.{ts,tsx}"],
      exclude: [
        "src/**/*.test.{ts,tsx}",
        "src/**/*.test-fixture.{ts,tsx}",
      ],
      thresholds: {
        statements: 100,
        branches: 100,
        functions: 100,
        lines: 100,
      },
    },
  },
});
