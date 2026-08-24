package main

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// parseRaw turns a readable literal into real SMTP data. Every line ending in
// a message on the wire is CRLF, and a MIME boundary matched against LF-only
// input behaves differently, so the tests must not use "\n".
func parseRaw(s string) []byte {
	return []byte(strings.ReplaceAll(s, "\n", "\r\n"))
}

func parseMsg(raw []byte) *Message {
	return &Message{ID: "test", From: "s@example.test", To: []string{"r@example.test"}, Raw: raw, Size: len(raw)}
}

var parsePNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0xff}

func TestParsePlainText(t *testing.T) {
	pm := Parse(parseMsg(parseRaw(`From: s@example.test
To: r@example.test
Subject: plain
Date: Mon, 02 Jan 2006 15:04:05 -0700
Content-Type: text/plain; charset=utf-8

hello body
`)), true)

	if pm.ParseError != "" {
		t.Fatalf("ParseError = %q, want empty", pm.ParseError)
	}
	if !strings.Contains(pm.Text, "hello body") {
		t.Errorf("Text = %q, want it to contain %q", pm.Text, "hello body")
	}
	if pm.HTML != "" {
		t.Errorf("HTML = %q, want empty", pm.HTML)
	}
	if len(pm.Attachments) != 0 {
		t.Errorf("Attachments = %v, want none", pm.Attachments)
	}
	if pm.Date == nil {
		t.Fatal("Date = nil, want the parsed Date header")
	}
	if got := pm.Date.UTC().Format("2006-01-02T15:04:05Z"); got != "2006-01-02T22:04:05Z" {
		t.Errorf("Date = %s, want 2006-01-02T22:04:05Z", got)
	}
}

// A missing or unparseable Date must stay nil rather than borrow ReceivedAt:
// the UI shows arrival time separately and conflating them invents a fact.
func TestParseDateAbsentOrBad(t *testing.T) {
	for _, tc := range []struct{ name, date string }{
		{"absent", ""},
		{"unparseable", "Date: yesterday afternoon\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pm := Parse(parseMsg(parseRaw("Subject: x\n"+tc.date+"Content-Type: text/plain\n\nbody\n")), true)
			if pm.Date != nil {
				t.Errorf("Date = %v, want nil", pm.Date)
			}
		})
	}
}

func TestParseMultipartAlternative(t *testing.T) {
	pm := Parse(parseMsg(parseRaw(`From: s@example.test
Subject: alt
Content-Type: multipart/alternative; boundary=BND

--BND
Content-Type: text/plain; charset=utf-8

the plain part
--BND
Content-Type: text/html; charset=utf-8

<p>the html part</p>
--BND--
`)), true)

	if pm.ParseError != "" {
		t.Fatalf("ParseError = %q, want empty", pm.ParseError)
	}
	if !strings.Contains(pm.Text, "the plain part") {
		t.Errorf("Text = %q, want the text/plain part", pm.Text)
	}
	if strings.Contains(pm.Text, "the html part") {
		t.Errorf("Text = %q, must not contain the HTML part", pm.Text)
	}
	if !strings.Contains(pm.HTML, "<p>the html part</p>") {
		t.Errorf("HTML = %q, want the text/html part", pm.HTML)
	}
}

// mail.CreateReader returns a NIL Reader for any error that is not an unknown
// charset (mail/reader.go:64), so touching mr.Header after the usual
// "if err != nil" panics. go-smtp recovers that panic but answers a retryable
// 421 and the message is lost -- the exact case ParseError exists for.
func TestParseHeaderlessBodyDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Parse panicked: %v", r)
		}
	}()

	pm := Parse(parseMsg(parseRaw("hello world\n")), true)
	if pm.ParseError == "" {
		t.Error("ParseError is empty, want the parse failure reported")
	}
	if pm.ID != "test" {
		t.Errorf("ID = %q, want the embedded Message still populated", pm.ID)
	}
	if pm.Headers == nil {
		t.Error("Headers = nil, want a non-nil map so the JSON encoder emits {}")
	}
}

// go-message files any part with Content-Disposition: inline as an inline part
// (mail/reader.go:104), so classifying by header type would splice raw PNG
// bytes into Text -- every Symfony or Laravel mail with a header image.
func TestParseInlineImageIsAttachment(t *testing.T) {
	raw := parseRaw(fmt.Sprintf(`From: s@example.test
Subject: related
Content-Type: multipart/related; boundary=BND

--BND
Content-Type: text/plain; charset=utf-8

look at the logo
--BND
Content-Type: image/png
Content-Disposition: inline
Content-ID: <logo>
Content-Transfer-Encoding: base64

%s
--BND--
`, base64.StdEncoding.EncodeToString(parsePNG)))

	pm := Parse(parseMsg(raw), true)
	if pm.ParseError != "" {
		t.Fatalf("ParseError = %q, want empty", pm.ParseError)
	}
	if len(pm.Attachments) != 1 {
		t.Fatalf("Attachments = %+v, want the inline image filed as one attachment", pm.Attachments)
	}
	if got := pm.Attachments[0].ContentType; got != "image/png" {
		t.Errorf("ContentType = %q, want image/png", got)
	}
	if got := pm.Attachments[0].Size; got != len(parsePNG) {
		t.Errorf("Size = %d, want %d", got, len(parsePNG))
	}
	if strings.Contains(pm.Text, "\x89PNG") {
		t.Errorf("Text contains raw PNG bytes: %q", pm.Text)
	}
	if !strings.Contains(pm.Text, "look at the logo") {
		t.Errorf("Text = %q, want the text/plain part", pm.Text)
	}
}

// A part declaring neither filename nor name would render a nameless,
// unexplainable row in the UI.
func TestParseAttachmentPlaceholderFilename(t *testing.T) {
	raw := parseRaw(`From: s@example.test
Subject: mixed
Content-Type: multipart/mixed; boundary=BND

--BND
Content-Type: text/plain

body
--BND
Content-Type: application/pdf; name="report.pdf"
Content-Disposition: attachment; filename="report.pdf"

PDFBYTES
--BND
Content-Type: application/octet-stream
Content-Disposition: attachment

NAMELESS
--BND--
`)

	pm := Parse(parseMsg(raw), true)
	if len(pm.Attachments) != 2 {
		t.Fatalf("Attachments = %+v, want 2", pm.Attachments)
	}
	if got := pm.Attachments[0].Filename; got != "report.pdf" {
		t.Errorf("Attachments[0].Filename = %q, want report.pdf", got)
	}
	if got := pm.Attachments[1].Filename; got != "attachment-1" {
		t.Errorf("Attachments[1].Filename = %q, want attachment-1", got)
	}
}

// Proves the charset side-effect import is live: without it the body reaches
// the JSON encoder as U+FFFD with no error anywhere.
func TestParseLatin1Body(t *testing.T) {
	pm := Parse(parseMsg(parseRaw("Subject: latin\nContent-Type: text/plain; charset=iso-8859-1\n\ncaf\xe9 au lait\n")), true)

	if pm.ParseError != "" {
		t.Fatalf("ParseError = %q, want empty", pm.ParseError)
	}
	if !strings.Contains(pm.Text, "café au lait") {
		t.Errorf("Text = %q, want it to contain %q", pm.Text, "café au lait")
	}
}

func TestParseHeaders(t *testing.T) {
	pm := Parse(parseMsg(parseRaw(`Received: from a.example.test by b.example.test; Mon, 02 Jan 2006 15:04:05 -0700
Received: from c.example.test by d.example.test; Mon, 02 Jan 2006 15:04:06 -0700
Subject: =?iso-8859-1?Q?caf=E9?= time
From: s@example.test
Content-Type: text/plain

body
`)), true)

	if pm.ParseError != "" {
		t.Fatalf("ParseError = %q, want empty", pm.ParseError)
	}
	if got := pm.Headers["Subject"]; len(got) != 1 || got[0] != "café time" {
		t.Errorf("Headers[Subject] = %q, want [\"café time\"]", got)
	}
	// A real message has several Received headers; keeping only the first
	// would hide the delivery path, which is half of why the header view
	// exists.
	got := pm.Headers["Received"]
	if len(got) != 2 {
		t.Fatalf("Headers[Received] = %q, want 2 values", got)
	}
	if !strings.Contains(got[0], "a.example.test") || !strings.Contains(got[1], "c.example.test") {
		t.Errorf("Headers[Received] = %q, want document order", got)
	}
}

func TestParseStripRemote(t *testing.T) {
	html := `<html><head>` +
		`<link rel="stylesheet" href="https://tracker.example/s.css">` +
		`<style>@import "https://tracker.example/i.css"; body{background:url(https://tracker.example/bg.png)}</style>` +
		`<meta http-equiv="refresh" content="0;url=https://tracker.example/r">` +
		`</head><body>` +
		`<img src="https://tracker.example/open.gif?id=123">` +
		`<img srcset="https://tracker.example/2x.gif 2x">` +
		`<img src="data:image/png;base64,iVBORw0KGgo=">` +
		`<img src="cid:logo">` +
		`<td background="https://tracker.example/tile.gif">` +
		`<div style="background-image:url('https://tracker.example/d.png')">x</div>` +
		`<script>fetch("https://tracker.example/js")</script>` +
		`</body></html>`

	raw := parseRaw("Subject: remote\nContent-Type: text/html; charset=utf-8\n\n" + strings.ReplaceAll(html, "\n", "") + "\n")

	stripped := Parse(parseMsg(raw), true)
	if stripped.ParseError != "" {
		t.Fatalf("ParseError = %q, want empty", stripped.ParseError)
	}
	if strings.Contains(stripped.HTML, "tracker") {
		t.Errorf("stripped HTML still references the tracker:\n%s", stripped.HTML)
	}
	if strings.Contains(strings.ToLower(stripped.HTML), "<script") {
		t.Errorf("stripped HTML still contains a script element:\n%s", stripped.HTML)
	}
	// data: is inert and self-contained, cid: resolves to nothing; stripping
	// them would break every legitimate embedded image for no gain.
	if !strings.Contains(stripped.HTML, "data:image/png;base64,iVBORw0KGgo=") {
		t.Errorf("stripped HTML dropped the data: URI:\n%s", stripped.HTML)
	}
	if !strings.Contains(stripped.HTML, "cid:logo") {
		t.Errorf("stripped HTML dropped the cid: reference:\n%s", stripped.HTML)
	}

	kept := Parse(parseMsg(raw), false)
	if !strings.Contains(kept.HTML, "https://tracker.example/open.gif?id=123") {
		t.Errorf("stripRemote=false must return the HTML untouched, got:\n%s", kept.HTML)
	}
	if !strings.Contains(kept.HTML, "<script>") {
		t.Errorf("stripRemote=false must return the HTML untouched, got:\n%s", kept.HTML)
	}
}

func TestParseStripRemoteHTMLCases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:    "img src",
			in:      `<img alt="a" src="https://tracker.example/x.gif" width="1">`,
			absent:  []string{"tracker"},
			present: []string{`alt="a"`, `width="1"`},
		},
		{
			name:   "protocol relative",
			in:     `<img src="//tracker.example/x.gif">`,
			absent: []string{"tracker"},
		},
		{
			name:   "relative path is stripped too",
			in:     `<img src="/local/x.gif">`,
			absent: []string{"/local/x.gif"},
		},
		{
			name:    "fragment survives",
			in:      `<a href="#anchor">x</a>`,
			present: []string{`href="#anchor"`},
		},
		{
			name:    "anchor href is not a subresource",
			in:      `<a href="https://example.test/page">x</a>`,
			present: []string{"https://example.test/page"},
		},
		{
			name:    "css url rewritten not deleted",
			in:      `<style>div{background:url(https://tracker.example/a.png)}</style>`,
			absent:  []string{"tracker"},
			present: []string{"url(about:blank)"},
		},
		{
			name:    "css data url survives",
			in:      `<style>div{background:url("data:image/gif;base64,R0lGOD")}</style>`,
			present: []string{"data:image/gif;base64,R0lGOD"},
		},
		{
			name:   "unterminated script",
			in:     `<body><script>fetch("https://tracker.example/j")`,
			absent: []string{"tracker", "<script"},
		},
		{
			name:   "svg xlink href",
			in:     `<svg><image xlink:href="https://tracker.example/x.svg"/></svg>`,
			absent: []string{"tracker"},
		},
		{
			name:   "unquoted src",
			in:     `<img src=https://tracker.example/x.gif>`,
			absent: []string{"tracker"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stripRemoteHTML(tc.in)
			for _, s := range tc.absent {
				if strings.Contains(strings.ToLower(got), strings.ToLower(s)) {
					t.Errorf("stripRemoteHTML(%q) = %q, must not contain %q", tc.in, got, s)
				}
			}
			for _, s := range tc.present {
				if !strings.Contains(got, s) {
					t.Errorf("stripRemoteHTML(%q) = %q, want it to keep %q", tc.in, got, s)
				}
			}
		})
	}
}

func TestParseAttachmentBytes(t *testing.T) {
	raw := parseRaw(fmt.Sprintf(`From: s@example.test
Subject: mixed
Content-Type: multipart/mixed; boundary=BND

--BND
Content-Type: text/plain

body
--BND
Content-Type: image/png; name="logo.png"
Content-Disposition: attachment; filename="logo.png"
Content-Transfer-Encoding: base64

%s
--BND--
`, base64.StdEncoding.EncodeToString(parsePNG)))

	m := parseMsg(raw)
	att, body, err := ParseAttachment(m, 0)
	if err != nil {
		t.Fatalf("ParseAttachment(0) error = %v", err)
	}
	if att.Filename != "logo.png" {
		t.Errorf("Filename = %q, want logo.png", att.Filename)
	}
	if string(body) != string(parsePNG) {
		t.Errorf("body = %v, want %v", body, parsePNG)
	}
	if att.Size != len(parsePNG) {
		t.Errorf("Size = %d, want %d", att.Size, len(parsePNG))
	}
	// The index must line up with ParsedMessage.Attachments, which is what the
	// UI hands back in the URL.
	pm := Parse(m, true)
	if len(pm.Attachments) != 1 || pm.Attachments[0] != att {
		t.Errorf("Parse attachments = %+v, want [%+v]", pm.Attachments, att)
	}

	for _, n := range []int{-1, 1, 99} {
		if _, _, err := ParseAttachment(m, n); err == nil {
			t.Errorf("ParseAttachment(%d) error = nil, want an error", n)
		}
	}
}

// NextPart reports a truncated multipart with a non-io.EOF error. Bailing on
// err != nil would discard every part already read, which is the opposite of
// what a trap is for.
func TestParseTruncatedMultipart(t *testing.T) {
	pm := Parse(parseMsg(parseRaw(`From: s@example.test
Subject: truncated
Content-Type: multipart/mixed; boundary=BND

--BND
Content-Type: text/plain

first part survives
--BND
Content-Type: application/pdf
Content-Disposition: attachment; filename="a.pdf"

PDFBYTES`)), true)

	if pm.ParseError == "" {
		t.Error("ParseError is empty, want the truncation reported")
	}
	if !strings.Contains(pm.Text, "first part survives") {
		t.Errorf("Text = %q, want the parts read before the truncation", pm.Text)
	}
	if len(pm.Attachments) != 1 || pm.Attachments[0].Filename != "a.pdf" {
		t.Errorf("Attachments = %+v, want the truncated part's metadata kept", pm.Attachments)
	}
}

// A corrupt base64 part fails at io.ReadAll, not at CreateReader; without
// capturing that the attachment silently records Size 0.
func TestParseCorruptBase64ReportsError(t *testing.T) {
	pm := Parse(parseMsg(parseRaw(`From: s@example.test
Subject: corrupt
Content-Type: multipart/mixed; boundary=BND

--BND
Content-Type: text/plain

body
--BND
Content-Type: application/pdf; name="broken.pdf"
Content-Disposition: attachment; filename="broken.pdf"
Content-Transfer-Encoding: base64

!!!!not base64 at all!!!!
--BND--
`)), true)

	if pm.ParseError == "" {
		t.Error("ParseError is empty, want the per-part decode failure reported")
	}
	if len(pm.Attachments) != 1 {
		t.Fatalf("Attachments = %+v, want the broken part still listed", pm.Attachments)
	}
}
