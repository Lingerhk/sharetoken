package mailer

import (
	"bytes"
	"errors"
	"io"
	"mime"
	netmail "net/mail"
	"strings"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // GBK, Big5, ISO-2022-JP… in mail from Chinese and other providers
	"github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
)

// Attachment describes one attachment of a parsed message.
type Attachment struct {
	Index    int    `json:"index"`
	Filename string `json:"filename"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
}

// Parsed is a received message prepared for reading. Mail is written by
// strangers, so the admin console only ever shows Text, as text: HTML is reduced
// to its text here and never reaches a browser as HTML.
type Parsed struct {
	From        string       `json:"from"`
	ReplyTo     string       `json:"reply_to,omitempty"`
	To          string       `json:"to"`
	Subject     string       `json:"subject"`
	Date        int64        `json:"date,omitempty"`
	Text        string       `json:"text"`
	FromHTML    bool         `json:"from_html,omitempty"` // Text was extracted from HTML
	Malformed   bool         `json:"malformed,omitempty"`
	Attachments []Attachment `json:"attachments"`
}

// maxShownText bounds what a message page carries.
const maxShownText = 512 << 10

// walk visits the leaf parts of raw. It stops without error at the first
// structure it cannot read, keeping what it got.
func walk(raw []byte, fn func(h mail.PartHeader, body io.Reader) error) (*mail.Header, error) {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return nil, err
	}
	if r == nil {
		return nil, err
	}
	defer r.Close()
	for {
		p, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			return &r.Header, nil
		}
		if err != nil && !message.IsUnknownCharset(err) {
			return &r.Header, err
		}
		if p == nil {
			return &r.Header, err
		}
		if err := fn(p.Header, p.Body); err != nil {
			return &r.Header, err
		}
	}
}

// Parse extracts what a person reads from a raw message.
func Parse(raw []byte) *Parsed {
	out := &Parsed{Attachments: []Attachment{}}
	var plain, htmlText strings.Builder
	index := 0
	hdr, err := walk(raw, func(h mail.PartHeader, body io.Reader) error {
		switch ph := h.(type) {
		case *mail.AttachmentHeader:
			name, _ := ph.Filename()
			typ, _, _ := ph.ContentType()
			n, _ := io.Copy(io.Discard, body)
			out.Attachments = append(out.Attachments, Attachment{Index: index, Filename: clean(name, 255), Type: clean(typ, 127), Size: n})
			index++
		case *mail.InlineHeader:
			typ, _, _ := ph.ContentType()
			switch typ {
			case "", "text/plain":
				b, _ := io.ReadAll(io.LimitReader(body, maxShownText))
				if plain.Len() > 0 {
					plain.WriteString("\n")
				}
				plain.Write(b)
			case "text/html":
				b, _ := io.ReadAll(io.LimitReader(body, 4*maxShownText))
				htmlText.WriteString(htmlToText(b))
			default: // an inline image is an attachment to whoever reads this as text
				n, _ := io.Copy(io.Discard, body)
				out.Attachments = append(out.Attachments, Attachment{Index: index, Filename: inlineName(ph), Type: clean(typ, 127), Size: n})
				index++
			}
		}
		return nil
	})
	if err != nil {
		out.Malformed = true
	}
	if hdr != nil {
		out.Subject, _ = hdr.Subject()
		out.Subject = clean(out.Subject, 1000)
		out.From = addressList(hdr, "From")
		out.ReplyTo = addressList(hdr, "Reply-To")
		out.To = addressList(hdr, "To")
		if d, err := hdr.Date(); err == nil {
			out.Date = d.Unix()
		}
	}
	out.Text = plain.String()
	if strings.TrimSpace(out.Text) == "" && htmlText.Len() > 0 {
		out.Text, out.FromHTML = htmlText.String(), true
	}
	if strings.TrimSpace(out.Text) == "" && hdr == nil {
		out.Text = string(raw) // not a readable message at all: show what arrived
	}
	out.Text = clean(strings.ReplaceAll(out.Text, "\r\n", "\n"), maxShownText)
	return out
}

func inlineName(h *mail.InlineHeader) string {
	if _, params, err := h.ContentDisposition(); err == nil && params["filename"] != "" {
		return clean(params["filename"], 255)
	}
	if _, params, err := h.ContentType(); err == nil && params["name"] != "" {
		return clean(params["name"], 255)
	}
	return "inline"
}

// ReadAttachment returns the content of attachment index of raw.
func ReadAttachment(raw []byte, index int) (*Attachment, []byte, error) {
	i := 0
	var found *Attachment
	var data []byte
	_, err := walk(raw, func(h mail.PartHeader, body io.Reader) error {
		var a Attachment
		switch ph := h.(type) {
		case *mail.AttachmentHeader:
			a.Filename, _ = ph.Filename()
			a.Type, _, _ = ph.ContentType()
		case *mail.InlineHeader:
			typ, _, _ := ph.ContentType()
			if typ == "" || typ == "text/plain" || typ == "text/html" {
				return nil
			}
			a.Filename, a.Type = inlineName(ph), typ
		default:
			return nil
		}
		if i == index {
			b, err := io.ReadAll(body)
			if err != nil {
				return err
			}
			a.Index, a.Size = i, int64(len(b))
			found, data = &a, b
			return io.EOF
		}
		i++
		return nil
	})
	if found != nil {
		return found, data, nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, err
	}
	return nil, nil, ErrNotFound
}

func addressList(h *mail.Header, key string) string {
	if as, err := h.AddressList(key); err == nil && len(as) > 0 {
		parts := make([]string, 0, len(as))
		for _, a := range as {
			parts = append(parts, displayAddress(a))
		}
		return clean(strings.Join(parts, ", "), 1000)
	}
	v, _ := h.Text(key)
	return clean(v, 1000)
}

// displayAddress shows a name in its own script, unlike Address.String.
func displayAddress(a *mail.Address) string {
	if a.Name == "" {
		return a.Address
	}
	return a.Name + " <" + a.Address + ">"
}

// htmlToText keeps the text of an HTML part: no scripts, styles, or markup.
func htmlToText(b []byte) string {
	z := html.NewTokenizer(bytes.NewReader(b))
	var out strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.TrimSpace(collapseBlank(out.String()))
		case html.TextToken:
			if skip == 0 {
				out.Write(z.Text())
			}
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			raw := strings.ToLower(string(z.Raw()))
			name, _ := z.TagName()
			switch {
			case strings.HasPrefix(raw, "<script"), strings.HasPrefix(raw, "<style"), strings.HasPrefix(raw, "<head"):
				skip++
			case strings.HasPrefix(raw, "</script"), strings.HasPrefix(raw, "</style"), strings.HasPrefix(raw, "</head"):
				if skip > 0 {
					skip--
				}
			}
			switch string(name) {
			case "br", "p", "div", "tr", "li", "h1", "h2", "h3", "h4", "table", "blockquote":
				out.WriteString("\n")
			}
		}
	}
}

func collapseBlank(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	blank := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			if blank++; blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// ReplyTarget returns the address an answer to a message goes to: Reply-To,
// else From. Empty when neither holds an address.
func ReplyTarget(raw []byte) string {
	msg, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	for _, k := range []string{"Reply-To", "From"} {
		dec := netmail.AddressParser{WordDecoder: &mime.WordDecoder{CharsetReader: charsetPassthrough}}
		if a, err := dec.Parse(msg.Header.Get(k)); err == nil {
			return strings.ToLower(a.Address)
		}
	}
	return ""
}

// charsetPassthrough: only the address is needed, the display name may stay garbled.
func charsetPassthrough(_ string, input io.Reader) (io.Reader, error) { return input, nil }

// headerSummary reads the header fields stored in columns.
type headerSummary struct {
	From, Subject, MessageID string
}

func summarize(raw []byte) headerSummary {
	var s headerSummary
	end := bytes.Index(raw, []byte("\r\n\r\n"))
	if end < 0 {
		end = bytes.Index(raw, []byte("\n\n"))
	}
	if end < 0 {
		end = len(raw)
	} else {
		end += 2
	}
	r, err := message.Read(bytes.NewReader(append(append([]byte{}, raw[:end]...), []byte("\r\n")...)))
	if r == nil || (err != nil && !message.IsUnknownCharset(err)) {
		return s
	}
	h := mail.Header{Header: r.Header}
	s.Subject, _ = h.Subject()
	s.From = addressList(&h, "From")
	s.MessageID = strings.TrimSpace(h.Get("Message-Id"))
	return s
}
