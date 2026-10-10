/**
 * Playwright smoke test: open the portal landing page and save a screenshot.
 * Extended tours (cache MISS/HIT, requester flows) can be added here as needed.
 */
import { chromium } from "playwright";
import fs from "fs";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const PORTAL_URL = process.env.PORTAL_URL || "http://localhost:5173";
const headless = process.env.HEADLESS !== "0";
const screenshotsDir = path.join(__dirname, "screenshots");

fs.mkdirSync(screenshotsDir, { recursive: true });

const browser = await chromium.launch({ headless });
const page = await browser.newPage();

try {
  await page.goto(PORTAL_URL, { waitUntil: "networkidle", timeout: 120000 });
  const out = path.join(screenshotsDir, "landing.png");
  await page.screenshot({ path: out, fullPage: true });
  console.log(`OK: ${out}`);
} finally {
  await browser.close();
}
