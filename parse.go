package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-message"
	// charset is imported for its Reader AND for its side effect: it installs
	// message.CharsetReader. Without it, a body labelled iso-8859-1 reaches a
	// Go string as raw bytes and encoding/json silently replaces them with
	// U+FFFD returning NO error, while subjects still decode correctly via
	// mime.WordDecoder -- mojibake bodies with no diagnostic anywhere.
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
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

var errNoSuchAttachment = errors.New("no such attachment")

// Parse decodes a stored message on demand. It never returns an error and
// never panics: whatever went wrong lands in ParseError and every field that
// could still be filled in is filled in, because a trap that hides the mail it
// could not parse is worse than useless -- that mail is exactly the one the
// developer is trying to look at.
func Parse(m *Message, stripRemote bool) ParsedMessage {
	pm := ParsedMessage{Message: *m, Headers: map[string][]string{}}

	mr, err := mail.CreateReader(bytes.NewReader(m.Raw))
	if mr == nil {
		// CreateReader returns a NIL Reader for every error except an unknown
		// charset (mail/reader.go:64), and a body whose first line is not a
		// header is one of them. Reaching for mr.Header here would panic;
		// go-smtp recovers the panic but answers a RETRYABLE 421, so the
		// sender loops forever on the one message this field exists for.
		if err == nil {
			err = errors.New("unparseable message")
		}
		pm.ParseError = err.Error()
		return pm
	}
	defer mr.Close()

	var errs []string
	if err != nil {
		// Non-nil alongside a non-nil Reader means an unknown charset, which
		// leaves the Reader fully usable. Record it and carry on.
		errs = append(errs, err.Error())
	}

	for f := mr.Header.Fields(); f.Next(); {
		k := f.Key()
		pm.Headers[k] = append(pm.Headers[k], decodeHeaderWords(f.Value()))
	}

	// A missing or unparseable Date is the common case in trapped mail, so it
	// stays nil rather than borrowing ReceivedAt -- the UI shows arrival time
	// separately and conflating the two would invent a fact.
	if d, derr := mr.Header.Date(); derr == nil && !d.IsZero() {
		pm.Date = &d
	}

	text, html, atts, _, perrs := readParts(mr, -1)
	pm.Text, pm.HTML, pm.Attachments = text, html, atts
	errs = append(errs, perrs...)

	if stripRemote {
		pm.HTML = stripRemoteHTML(pm.HTML)
	}
	if len(errs) > 0 {
		pm.ParseError = strings.Join(errs, "; ")
	}
	return pm
}

// ParseAttachment returns one attachment's metadata and bytes by index.
//
// The caller serves these bytes as application/octet-stream with nosniff --
// never the declared Content-Type, which would make a text/html attachment
// stored XSS on the mailbox origin.
func ParseAttachment(m *Message, n int) (Attachment, []byte, error) {
	if n < 0 {
		return Attachment{}, nil, errNoSuchAttachment
	}
	mr, err := mail.CreateReader(bytes.NewReader(m.Raw))
	if mr == nil {
		if err == nil {
			err = errNoSuchAttachment
		}
		return Attachment{}, nil, err
	}
	defer mr.Close()

	_, _, atts, body, _ := readParts(mr, n)
	if n >= len(atts) {
		return Attachment{}, nil, errNoSuchAttachment
	}
	// A part that failed mid-decode yields the bytes read before the error.
	// Handing those over with the Size that matches beats a 404 on an
	// attachment the message demonstrably contains.
	return atts[n], body, nil
}

// readParts walks every MIME part once. keep is the index of the attachment
// whose decoded bytes are retained; -1 retains none, which is what Parse wants
// -- holding every attachment in memory would double what a message costs at
// exactly the moment somebody opens it.
func readParts(mr *mail.Reader, keep int) (text, html string, atts []Attachment, kept []byte, errs []string) {
	var tb, hb strings.Builder

	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if p == nil {
			// A truncated multipart -- a missing closing boundary -- reports
			// "multipart: NextPart: EOF", which is NOT io.EOF, and returns no
			// part. Returning nothing here would throw away every part already
			// read, so record it and keep what we have.
			if err != nil {
				errs = append(errs, err.Error())
			}
			break
		}
		if err != nil {
			errs = append(errs, err.Error())
		}

		// Classify by CONTENT-TYPE, never by the Inline/Attachment header type
		// go-message hands back. reader.go:104 files anything with
		// Content-Disposition: inline as inline, so an embedded cid: logo --
		// every Symfony or Laravel mail with a header image -- would otherwise
		// land in Text as raw PNG bytes.
		var mh *message.Header
		switch h := p.Header.(type) {
		case *mail.InlineHeader:
			mh = &h.Header
		case *mail.AttachmentHeader:
			mh = &h.Header
		}

		// Unknown until proven text: a part whose Content-Type cannot be read
		// becomes an attachment rather than being spliced into the body.
		mediaType := "application/octet-stream"
		var ctParams map[string]string
		if mh != nil {
			t, params, cerr := mh.ContentType()
			if cerr != nil {
				errs = append(errs, cerr.Error())
			} else {
				mediaType, ctParams = strings.ToLower(t), params
			}
		}

		// A corrupt base64 part fails HERE, not at CreateReader. Without this
		// the attachment silently records Size 0 and nobody is told why.
		b, rerr := io.ReadAll(p.Body)
		if rerr != nil {
			errs = append(errs, rerr.Error())
		}

		switch mediaType {
		case "text/plain":
			if tb.Len() > 0 {
				tb.WriteString("\n")
			}
			tb.Write(b)
		case "text/html":
			if hb.Len() > 0 {
				hb.WriteString("\n")
			}
			hb.Write(b)
		default:
			n := len(atts)
			name := partFilename(mh, ctParams)
			if name == "" {
				// A nameless row in the UI is unclickable and unexplainable.
				name = fmt.Sprintf("attachment-%d", n)
			}
			atts = append(atts, Attachment{Filename: name, ContentType: mediaType, Size: len(b)})
			if n == keep {
				kept = b
			}
		}
	}

	return tb.String(), hb.String(), atts, kept, errs
}

// partFilename mirrors mail.AttachmentHeader.Filename, which is unreachable for
// a part go-message filed as inline -- and an inline image is precisely the
// part that needs a name.
func partFilename(h *message.Header, ctParams map[string]string) string {
	if h == nil {
		return ""
	}
	if _, params, err := h.ContentDisposition(); err == nil {
		if f := params["filename"]; f != "" {
			return decodeHeaderWords(f)
		}
	}
	// "name" on Content-Type is discouraged by RFC 2183 and still what a lot
	// of PHP mailers emit.
	return decodeHeaderWords(ctParams["name"])
}

// decodeHeaderWords decodes RFC 2047 encoded-words using the same CharsetReader
// smtp.go's scanSubject uses, so a Subject in the list can never disagree with
// the same Subject in the detail view.
func decodeHeaderWords(v string) string {
	dec := mime.WordDecoder{CharsetReader: charset.Reader}
	s, err := dec.DecodeHeader(v)
	if err != nil {
		return v
	}
	return s
}

// Patterns for stripRemoteHTML. Each one targets a construct that causes the
// browser to fetch something; none of them attempts to understand HTML.
var (
	reScriptBlock = regexp.MustCompile(`(?is)<script\b[^>]*>.*?(?:</script\s*>|$)`)
	reScriptTag   = regexp.MustCompile(`(?is)</?script\b[^>]*>`)
	reMetaRefresh = regexp.MustCompile(`(?is)<meta\b[^>]*http-equiv\s*=\s*["']?refresh\b[^>]*>`)
	reLinkTag     = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	reHrefAttr    = regexp.MustCompile(`(?is)\shref\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'>]+)`)
	reFetchAttr   = regexp.MustCompile(`(?is)\s(?:srcset|src|background|poster|data|xlink:href)\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"'>]+)`)
	reCSSURL      = regexp.MustCompile(`(?is)url\(\s*(?:"[^"]*"|'[^']*'|[^)"']*)\s*\)`)
	reCSSImport   = regexp.MustCompile(`(?is)(@import\s+)("[^"]*"|'[^']*')`)
)

// stripRemoteHTML removes references to remote subresources from an HTML body.
//
// This is the PRIMARY control. A trapped mail containing
// <img src="https://tracker.example/open.gif?id=123"> fires the instant a
// developer clicks the message, leaking "this mail was opened" and the
// developer's IP to a third party -- from a tool whose whole premise is that it
// sends nothing. The iframe csp= attribute the UI also sets is Chrome-only, so
// it cannot carry this on its own.
//
// It is deliberately NOT a sanitiser and pulls in no dependency: it is a
// targeted pass over the attributes and CSS constructs that cause a fetch. Only
// data:, cid: and pure fragments survive -- data: is inert and self-contained,
// cid: resolves to nothing. Relative and protocol-relative URLs go too, because
// a broken render is acceptable and a fired pixel is not.
func stripRemoteHTML(h string) string {
	if h == "" {
		return h
	}

	// The sandbox already blocks execution; removing scripts anyway costs
	// nothing and means a sandbox bug cannot reveal one.
	h = reScriptBlock.ReplaceAllString(h, "")
	h = reScriptTag.ReplaceAllString(h, "")
	// A meta refresh navigates the frame itself, which is a fetch like any
	// other.
	h = reMetaRefresh.ReplaceAllString(h, "")

	h = reLinkTag.ReplaceAllStringFunc(h, func(tag string) string {
		return reHrefAttr.ReplaceAllStringFunc(tag, dropAttrIfRemote)
	})
	h = reFetchAttr.ReplaceAllStringFunc(h, dropAttrIfRemote)

	// url() is rewritten across the whole document rather than only inside
	// <style> and style="": one pass covers both, plus the unquoted attribute
	// form, and the only cost is mangling a literal "url(...)" in visible
	// prose.
	h = reCSSURL.ReplaceAllStringFunc(h, func(s string) string {
		open := strings.IndexByte(s, '(')
		if inertURL(unquote(s[open+1 : len(s)-1])) {
			return s
		}
		return "url(about:blank)"
	})
	h = reCSSImport.ReplaceAllStringFunc(h, func(s string) string {
		m := reCSSImport.FindStringSubmatch(s)
		if inertURL(unquote(m[2])) {
			return s
		}
		return m[1] + `""`
	})

	return h
}

// dropAttrIfRemote deletes a whole attribute rather than emptying it: src=""
// still resolves against the document URL in some browsers, and a leftover
// URL in the markup would be readable even though it never fired.
func dropAttrIfRemote(attr string) string {
	eq := strings.IndexByte(attr, '=')
	if eq < 0 {
		return attr
	}
	name := strings.ToLower(strings.TrimSpace(attr[:eq]))
	value := unquote(attr[eq+1:])

	ok := inertURL(value)
	if name == "srcset" {
		ok = srcsetInert(value)
	}
	if ok {
		return attr
	}
	return " "
}

// inertURL is an allow-list, not a block-list: anything it does not recognise
// is treated as remote. Entity- and whitespace-obfuscated schemes therefore
// fail closed instead of slipping through.
func inertURL(u string) bool {
	u = strings.TrimSpace(u)
	if u == "" || strings.HasPrefix(u, "#") {
		return true
	}
	lower := strings.ToLower(u)
	return strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "cid:")
}

// srcsetInert errs toward stripping. A candidate list is comma-separated, but a
// data: URI legally contains commas, so the list cannot be split naively; only
// a single inert candidate with an optional descriptor is kept.
func srcsetInert(v string) bool {
	v = strings.TrimSpace(v)
	if i := strings.IndexAny(v, " \t\r\n"); i >= 0 {
		if strings.Contains(v[i:], ",") {
			return false
		}
		v = v[:i]
	}
	return inertURL(v)
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
