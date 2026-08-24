import { test, expect } from '@playwright/test'
import { sendFixture, clearMailbox, expectRowCount, count } from '../helpers/mailbox.js'

// E2E acceptance 2: unauthenticated and wrongly-authenticated mail never
// reaches the mailbox.
//
// The positive barrier at the end is the whole point. Asserting only "the list
// is empty" after the two rejected attempts would pass on a build with NO auth
// gate at all, because the assertion can run before a delivery would have shown
// up. Sending one message that SHOULD arrive, and then asserting the count is
// exactly one, is what makes the two rejections meaningful.
test('unauthenticated and wrong-password mail is refused; only the authenticated message lands', async ({ page }) => {
  await clearMailbox(page)

  const noAuth = sendFixture({ subject: 'should never arrive (no auth)', auth: 'none' })
  expect(noAuth.ok, 'sending without AUTH should have been refused, but it succeeded').toBe(false)
  expect(noAuth.output).toMatch(/50[0-9]|530|authenticate/i)

  const wrongPass = sendFixture({
    subject: 'should never arrive (wrong password)',
    password: 'definitely-not-the-password',
  })
  expect(wrongPass.ok, 'sending with a wrong password should have been refused').toBe(false)
  // 535 is a PERMANENT failure. go-smtp's default for a SASL error is 454, a
  // temporary one that mail libraries retry forever -- so a typo'd password
  // would hang a consuming app instead of failing it. This asserts the override.
  expect(wrongPass.output).toContain('535')

  // The positive barrier.
  const good = sendFixture({ subject: 'the only message that should arrive' })
  expect(good.ok, `authenticated send failed:\n${good.output}`).toBe(true)

  await page.goto('/')
  await expectRowCount(page, 1)
  await expect(count(page)).toHaveText('1')
  await expect(page.locator('#messageList li .msg-subject')).toHaveText(
    'the only message that should arrive',
  )
})
