import { test, expect } from '@playwright/test'
import {
  sendFixture, clearMailbox, expectRowCount, openMessage,
  count, bodyText, bodyHtml, htmlSrcdoc,
} from '../helpers/mailbox.js'
import {
  FIXTURE_FROM, FIXTURE_TEXT_MARKER, FIXTURE_HTML_MARKER, FIXTURE_TRACKER_HOST,
} from '../fixtures.js'

// E2E acceptance 1: a message delivered over authenticated SMTPS is visible in
// the mailbox, and both body views render.
test('an authenticated SMTPS message appears in the mailbox and renders', async ({ page }) => {
  await clearMailbox(page)

  const subject = 'delivery spec ' + Date.now()
  const sent = sendFixture({ subject })
  expect(sent.ok, `fixture send failed:\n${sent.output}`).toBe(true)

  await page.goto('/')
  await expectRowCount(page, 1)
  await expect(count(page)).toHaveText('1')

  const row = page.locator('#messageList li').first()
  await expect(row.locator('.msg-from')).toHaveText(FIXTURE_FROM)
  await expect(row.locator('.msg-subject')).toHaveText(subject)

  await openMessage(page, 0)
  await expect(page.locator('#detailSubject')).toHaveText(subject)
  await expect(page.locator('#detailFrom')).toContainText(FIXTURE_FROM)

  // The text/plain part.
  await expect(bodyText(page)).toContainText(FIXTURE_TEXT_MARKER)

  // The text/html part, in the sandboxed frame.
  await page.locator('#tabHtml').click()
  await expect(bodyHtml(page)).toBeVisible()

  // sandbox must be present AND empty: any allow-token would re-enable script
  // execution for attacker-controlled markup inside the mailbox origin.
  await expect(bodyHtml(page)).toHaveAttribute('sandbox', '')

  const srcdoc = await htmlSrcdoc(page)
  expect(srcdoc).toContain(FIXTURE_HTML_MARKER)
})

// The remote-content control. The fixture's HTML carries an <img> pointing at
// a remote host; a mailtrap that fetched it would leak "this mail was opened"
// plus the developer's IP to a third party.
test('remote images are stripped by default and restored on request', async ({ page }) => {
  await clearMailbox(page)

  const subject = 'remote spec ' + Date.now()
  const sent = sendFixture({ subject })
  expect(sent.ok, `fixture send failed:\n${sent.output}`).toBe(true)

  await page.goto('/')
  await expectRowCount(page, 1)
  await openMessage(page, 0)
  await page.locator('#tabHtml').click()

  expect(await htmlSrcdoc(page)).not.toContain(FIXTURE_TRACKER_HOST)

  // Explicitly opting in brings it back, which is what proves the default was
  // a deliberate strip and not simply a message that never had the image.
  const reloaded = page.waitForResponse(
    (res) => res.url().includes('remote=1') && res.status() === 200,
  )
  await page.locator('#loadRemote').click()
  await reloaded
  await expect
    .poll(async () => await htmlSrcdoc(page), { timeout: 10000 })
    .toContain(FIXTURE_TRACKER_HOST)
})
