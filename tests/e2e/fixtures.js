// Shared between the specs and the Go fixture sender (tests/send), which is
// invoked with these values as env overrides. Keeping them here means a spec
// and the message it asserts on cannot drift apart.

export const FIXTURE_FROM = 'e2e-sender@example.test'
export const FIXTURE_TO = 'e2e-recipient@example.test'

/** Present in the fixture's text/plain part. */
export const FIXTURE_TEXT_MARKER = "devmail's tests/send tool"

/** Present in the fixture's text/html part, and only there. */
export const FIXTURE_HTML_MARKER = '<strong>'

/**
 * The host of the remote image the fixture's HTML part carries. The mailbox
 * strips remote subresources by default, so this must NOT appear in the
 * rendered srcdoc unless remote loading is explicitly turned on.
 */
export const FIXTURE_TRACKER_HOST = 'tracker.example'
