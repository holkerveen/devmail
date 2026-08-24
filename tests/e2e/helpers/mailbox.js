// Thin helpers wrapping the mailbox UI's stable element ids (see
// public/index.html) and the Go fixture sender. Specs go through this layer
// rather than inlining selectors, so a DOM change is a one-file fix.

import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { expect } from '@playwright/test'
import { FIXTURE_FROM, FIXTURE_TO } from '../fixtures.js'

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..')

/**
 * Deliver a message over authenticated SMTPS by running the same Go tool that
 * `./devmail.sh send` runs. There is no Node SMTP client here on purpose: the
 * shipped image is Go-only, and a second implementation of the client would be
 * a second thing to keep correct.
 *
 * @param {object} [opts]
 * @param {string} [opts.subject]
 * @param {'plain'|'none'} [opts.auth]  'none' skips AUTH entirely
 * @param {string} [opts.password]      override to force a rejection
 * @returns {{ok: boolean, output: string}}
 */
export function sendFixture({ subject, auth = 'plain', password } = {}) {
  const env = {
    ...process.env,
    FROM: FIXTURE_FROM,
    TO: FIXTURE_TO,
    AUTH: auth,
  }
  if (subject) env.SUBJECT = subject
  if (password !== undefined) env.SMTP_PASSWORD = password

  try {
    const output = execFileSync('go', ['run', './tests/send'], {
      cwd: repoRoot,
      env,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    return { ok: true, output }
  } catch (err) {
    // A non-zero exit is the expected outcome for the rejection cases, so it
    // is returned rather than thrown; the spec decides whether it is a failure.
    return { ok: false, output: String(err.stdout ?? '') + String(err.stderr ?? '') }
  }
}

/** Empty the mailbox so a spec starts from a known state. */
export async function clearMailbox(page) {
  await page.goto('/')
  await page.evaluate(() => fetch('/api/messages', { method: 'DELETE' }))
}

/** @param {import('@playwright/test').Page} page */
export const messageList = (page) => page.locator('#messageList')
/** @param {import('@playwright/test').Page} page */
export const messageRows = (page) => page.locator('#messageList li')
/** @param {import('@playwright/test').Page} page */
export const count = (page) => page.locator('#count')
/** @param {import('@playwright/test').Page} page */
export const empty = (page) => page.locator('#empty')
/** @param {import('@playwright/test').Page} page */
export const detail = (page) => page.locator('#detail')
/** @param {import('@playwright/test').Page} page */
export const bodyText = (page) => page.locator('#bodyText')
/** @param {import('@playwright/test').Page} page */
export const bodyHtml = (page) => page.locator('#bodyHtml')

/**
 * Wait until the list holds exactly n rows. The UI polls every 2s, so this
 * waits on the real signal rather than sleeping.
 */
export async function expectRowCount(page, n) {
  await expect(messageRows(page)).toHaveCount(n, { timeout: 15000 })
}

/** Open the message at list position i and wait for its detail to render. */
export async function openMessage(page, i = 0) {
  const row = messageRows(page).nth(i)
  const responsePromise = page.waitForResponse(
    (res) => /\/api\/messages\/[^/]+$/.test(new URL(res.url()).pathname) && res.status() === 200,
  )
  await row.locator('a').click()
  await responsePromise
  await expect(detail(page)).toBeVisible()
}

/**
 * The HTML body as the browser will actually render it.
 *
 * Read from the srcdoc ATTRIBUTE rather than through frameLocator(): the frame
 * is sandbox="" with no allow-scripts, and Playwright's frameLocator runs its
 * selector engine inside the target frame, which needs script execution. This
 * assertion also happens to be the one that catches a srcdoc escaping bug,
 * since it sees exactly the string that was handed to the attribute.
 */
export async function htmlSrcdoc(page) {
  return await bodyHtml(page).getAttribute('srcdoc')
}
