#!/usr/bin/env bash
# playwright-driver.sh — browser-based driver for consumer app subscriptions
#
# Usage:
#   playwright-driver.sh --app <chatgpt|notebooklm|gemini-app> \
#                        --prompt <text> \
#                        [--profile-dir <path>] \
#                        [--headless] \
#                        [--timeout <seconds>]
#
# Runs a Playwright (Node.js) automation script against a consumer web app
# using a persistent browser profile so login sessions survive across runs.
#
# Supported apps:
#   chatgpt      — chat.openai.com (OpenAI Plus subscription)
#   notebooklm   — notebooklm.google.com (Google AI Premium)
#   gemini-app   — gemini.google.com (Google AI Premium)
#
# Exit codes:
#   0 — success; response written to stdout
#   1 — missing dependency or invalid argument
#   2 — browser session error (login required, CAPTCHA, etc.)
#   3 — timeout waiting for response
set -euo pipefail

# ── Defaults ─────────────────────────────────────────────────────────────────
APP=""
PROMPT_TEXT=""
PROFILE_DIR="${BROWSER_PROFILE_DIR:-$HOME/.octi-pulpo/browser-profiles}"
HEADLESS="${PLAYWRIGHT_HEADLESS:-true}"
TIMEOUT="${PLAYWRIGHT_TIMEOUT:-120}"

# ── Arg parsing ───────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
    case "$1" in
        --app)        APP="$2";          shift 2 ;;
        --prompt)     PROMPT_TEXT="$2";  shift 2 ;;
        --profile-dir) PROFILE_DIR="$2"; shift 2 ;;
        --headless)   HEADLESS="true";   shift ;;
        --no-headless) HEADLESS="false"; shift ;;
        --timeout)    TIMEOUT="$2";      shift 2 ;;
        *) echo "playwright-driver: unknown argument: $1" >&2; exit 1 ;;
    esac
done

# ── Validation ────────────────────────────────────────────────────────────────
if [[ -z "$APP" ]]; then
    echo "playwright-driver: --app is required (chatgpt|notebooklm|gemini-app)" >&2
    exit 1
fi
if [[ -z "$PROMPT_TEXT" ]]; then
    echo "playwright-driver: --prompt is required" >&2
    exit 1
fi
case "$APP" in
    chatgpt|notebooklm|gemini-app) ;;
    *)
        echo "playwright-driver: unsupported app '$APP'. Use: chatgpt, notebooklm, gemini-app" >&2
        exit 1
        ;;
esac

# ── Dependency check ──────────────────────────────────────────────────────────
if ! command -v node &>/dev/null; then
    echo "playwright-driver: node is required (install via nvm or system package manager)" >&2
    exit 1
fi
if ! node -e "require('@playwright/test')" 2>/dev/null && \
   ! node -e "require('playwright')" 2>/dev/null; then
    echo "playwright-driver: playwright not installed. Run: npm install -g playwright && npx playwright install chromium" >&2
    exit 1
fi

# ── Profile directory ─────────────────────────────────────────────────────────
APP_PROFILE="$PROFILE_DIR/$APP"
mkdir -p "$APP_PROFILE"

# ── Inline Playwright script ──────────────────────────────────────────────────
# Written to a temp file and executed by Node.js. The script is self-contained
# so playwright-driver.sh has no external JS file dependency.
SCRIPT=$(mktemp /tmp/playwright-driver-XXXXXX.mjs)
trap 'rm -f "$SCRIPT"' EXIT

cat > "$SCRIPT" << 'PLAYWRIGHT_SCRIPT'
import { chromium } from 'playwright';
import { setTimeout as sleep } from 'timers/promises';

const app         = process.env.PD_APP;
const promptText  = process.env.PD_PROMPT;
const profileDir  = process.env.PD_PROFILE_DIR;
const headless    = process.env.PD_HEADLESS !== 'false';
const timeoutMs   = parseInt(process.env.PD_TIMEOUT, 10) * 1000;

const APP_CONFIG = {
    'chatgpt': {
        url: 'https://chat.openai.com/',
        promptSelector: '#prompt-textarea',
        sendSelector: 'button[data-testid="send-button"]',
        responseSelector: '[data-message-author-role="assistant"]:last-child .markdown',
        loginIndicator: 'button[data-testid="send-button"]',
    },
    'notebooklm': {
        url: 'https://notebooklm.google.com/',
        promptSelector: 'textarea[placeholder]',
        sendSelector: 'button[aria-label="Submit"]',
        responseSelector: '.response-content',
        loginIndicator: 'textarea[placeholder]',
    },
    'gemini-app': {
        url: 'https://gemini.google.com/',
        promptSelector: 'rich-textarea .ql-editor',
        sendSelector: 'button.send-button',
        responseSelector: 'message-content model-response:last-child',
        loginIndicator: 'rich-textarea',
    },
};

const cfg = APP_CONFIG[app];
if (!cfg) {
    console.error(`playwright-driver: unknown app: ${app}`);
    process.exit(1);
}

const context = await chromium.launchPersistentContext(profileDir, {
    headless,
    args: [
        '--disable-blink-features=AutomationControlled',
        '--no-sandbox',
    ],
    viewport: { width: 1280, height: 900 },
});

const page = context.pages()[0] ?? await context.newPage();

try {
    await page.goto(cfg.url, { waitUntil: 'domcontentloaded', timeout: timeoutMs });

    // Check if logged in by waiting for the login indicator element.
    try {
        await page.waitForSelector(cfg.loginIndicator, { timeout: 15000 });
    } catch {
        console.error(`playwright-driver: not logged in to ${app}. Open a headed session and sign in first.`);
        process.exit(2);
    }

    // Type the prompt.
    await page.click(cfg.promptSelector);
    await page.fill(cfg.promptSelector, promptText);
    await page.click(cfg.sendSelector);

    // Wait for the response to appear and stabilise (stop growing).
    let lastLength = 0;
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        await sleep(2000);
        const responseEl = await page.$(cfg.responseSelector);
        if (!responseEl) continue;
        const text = await responseEl.innerText();
        if (text.length > 0 && text.length === lastLength) {
            // Response has stabilised — print and exit.
            process.stdout.write(text);
            process.exit(0);
        }
        lastLength = text.length;
    }

    console.error('playwright-driver: timed out waiting for response');
    process.exit(3);
} finally {
    await context.close();
}
PLAYWRIGHT_SCRIPT

# ── Execute ───────────────────────────────────────────────────────────────────
PD_APP="$APP" \
PD_PROMPT="$PROMPT_TEXT" \
PD_PROFILE_DIR="$APP_PROFILE" \
PD_HEADLESS="$HEADLESS" \
PD_TIMEOUT="$TIMEOUT" \
node --input-type=module < "$SCRIPT"
