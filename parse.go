package main

import (
	"time"
	// Blank-imported for its side effect: it installs message.CharsetReader.
	// Without it, a body labelled iso-8859-1 reaches a Go string as raw bytes
	// and encoding/json silently replaces them with U+FFFD returning NO error,
	// while subjects still decode correctly via mime.WordDecoder -- mojibake
	// bodies with no diagnostic anywhere.
	_ "github.com/emersion/go-message/charset"
)

// Attachment is metadata only; the bytes are fetched separately so the detail
// response stays small.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
}

// ParsedMessage is the on-demand form: everything a stored Message deliberately
// does not keep.
type ParsedMessage struct {
	Message
	Headers     map[string][]string `json:"headers"`
	Date        *time.Time          `json:"date,omitempty"`
	Text        string              `json:"text,omitempty"`
	HTML        string              `json:"html,omitempty"`
	Attachments []Attachment        `json:"attachments,omitempty"`
	ParseError  string              `json:"parseError,omitempty"`
}

// Parse decodes a stored message on demand.
//
// TODO(wave-1): full implementation. Contract the implementation must honour:
//
//   - mail.CreateReader returns a NIL Reader for any error that is not an
//     unknown charset. A header-less body returns (nil, err), so the natural
//     "if err != nil { record it }" followed by mr.Header.Subject() nil-derefs.
//     go-smtp recovers the panic but kills the connection with a RETRYABLE 421
//     and the message is lost -- the exact case this ParseError field exists
//     for. Never dereference the Reader without checking it for nil first.
//
//   - Classify parts by CONTENT-TYPE, not by go-message's Inline/Attachment
//     header type. go-message files any part with Content-Disposition: inline
//     as an inline part, so an embedded cid: logo -- every Symfony/Laravel mail
//     with a header image -- would land in Text as raw PNG bytes. Only
//     text/plain becomes Text, only text/html becomes HTML, everything else is
//     an Attachment regardless of disposition.
//
//   - A part with no filename gets a placeholder ("attachment-N"), or the UI
//     renders a nameless row.
//
//   - A corrupt base64 part errors at io.ReadAll(p.Body), NOT at CreateReader,
//     so per-part read errors must be captured or Size silently records 0.
//
//   - When stripRemote is true, remove remote src/href/url() from HTML so a
//     tracking pixel cannot fire when the developer opens the message. This is
//     the PRIMARY control: the iframe csp= attribute is Chrome-only.
func Parse(m *Message, stripRemote bool) ParsedMessage {
	return ParsedMessage{
		Message:    *m,
		Headers:    map[string][]string{},
		ParseError: "not implemented",
	}
}

// ParseAttachment returns one attachment's metadata and bytes by index.
//
// TODO(wave-1): full implementation. The caller serves these bytes as
// application/octet-stream with nosniff -- never the declared Content-Type,
// which would make a text/html attachment stored XSS on the mailbox origin.
func ParseAttachment(m *Message, n int) (Attachment, []byte, error) {
	return Attachment{}, nil, errNotImplemented
}

var errNotImplemented = errStr("not implemented")

type errStr string

func (e errStr) Error() string { return string(e) }
