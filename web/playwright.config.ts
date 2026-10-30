import { defineConfig, devices } from "@playwright/test";
export default defineConfig({
  testDir: "./tests",
  fullyParallel: true,
  workers: 2,
  timeout: 35_000,
  expect: { timeout: 15_000 },
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [["list"], ["html", { open: "never" }]],
  use: {
    baseURL: "http://127.0.0.1:7311",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "desktop",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1440, height: 1000 },
      },
    },
    {
      name: "mobile-dark-reduced-motion",
      use: {
        ...devices["Pixel 7"],
        colorScheme: "dark",
        reducedMotion: "reduce",
      },
    },
  ],
  webServer: {
    command:
      "go run ../cmd/darsena serve --demo --addr 127.0.0.1:7311 --web dist",
    url: "http://127.0.0.1:7311/api/v1/status",
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
});
