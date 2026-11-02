import { defineConfig, devices } from "@playwright/test";
const port = process.env.DARSENA_E2E_PORT || "7311";
const baseURL = `http://127.0.0.1:${port}`;
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
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "desktop",
      use: {
        ...devices["Desktop Chrome"],
        viewport: { width: 1440, height: 900 },
      },
    },
    {
      name: "mobile-dark-reduced-motion",
      use: {
        ...devices["Pixel 7"],
        viewport: { width: 390, height: 844 },
        colorScheme: "dark",
        reducedMotion: "reduce",
      },
    },
  ],
  webServer: {
    command: `go run ../cmd/darsena serve --demo --addr 127.0.0.1:${port} --web dist`,
    url: `${baseURL}/api/v1/status`,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
});
