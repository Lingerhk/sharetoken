package mailer

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	netmail "net/mail"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/dkim"
)

// DKIMSigner signs outgoing mail for a domain.
type DKIMSigner struct {
	Domain   string
	Selector string
	Key      crypto.Signer
}

// signedHeaders are covered by the DKIM signature. From is listed twice so that
// a second From cannot be added later without breaking it.
var signedHeaders = []string{"From", "From", "To", "Subject", "Date", "Message-ID", "Reply-To", "In-Reply-To", "References", "MIME-Version", "Content-Type"}

// Sign prepends a DKIM-Signature to raw.
func (d *DKIMSigner) Sign(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	err := dkim.Sign(&out, bytes.NewReader(raw), &dkim.SignOptions{
		Domain: d.Domain, Selector: d.Selector, Signer: d.Key,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed, BodyCanonicalization: dkim.CanonicalizationRelaxed,
		HeaderKeys: signedHeaders,
	})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// GenerateDKIMKey writes a new 2048-bit RSA key (mode 0600). It refuses to
// overwrite one: the published DNS record belongs to the existing key.
func GenerateDKIMKey(path string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// LoadDKIMKey reads a PEM key written by GenerateDKIMKey.
func LoadDKIMKey(path string) (crypto.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s: not a PEM file", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("%s: not a signing key", path)
	}
	return signer, nil
}

// DKIMRecord is the TXT record value that publishes key's public half.
func DKIMRecord(key crypto.Signer) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return "", err
	}
	return "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der), nil
}

// headerText removes what could start a new header from a header value.
func headerText(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}

// newMessageID makes a Message-ID at domain.
func newMessageID(domain string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "<" + hex.EncodeToString(b) + "@" + domain + ">", nil
}

// Compose renders m as an RFC 5322 message.
func Compose(m *OutMail, messageID string, now time.Time) (envelopeFrom string, raw []byte, err error) {
	from, err := netmail.ParseAddress(headerText(m.From))
	if err != nil {
		return "", nil, fmt.Errorf("sender %q: %w", m.From, err)
	}
	to, err := netmail.ParseAddress(headerText(m.To))
	if err != nil {
		return "", nil, fmt.Errorf("recipient %q: %w", m.To, err)
	}
	var b bytes.Buffer
	hdr := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	hdr("From", from.String())
	hdr("To", to.String())
	hdr("Subject", mime.QEncoding.Encode("utf-8", headerText(m.Subject)))
	hdr("Date", now.Format(time.RFC1123Z))
	hdr("Message-ID", messageID)
	if m.ReplyTo != "" {
		if rt, err := netmail.ParseAddress(headerText(m.ReplyTo)); err == nil {
			hdr("Reply-To", rt.String())
		}
	}
	if id := headerText(m.InReplyTo); id != "" {
		hdr("In-Reply-To", id)
		hdr("References", id)
	}
	if m.Kind == KindVerify || m.Kind == KindNotice {
		hdr("Auto-Submitted", "auto-generated") // keeps autoresponders from answering
	}
	hdr("MIME-Version", "1.0")
	if m.HTML == "" {
		hdr("Content-Type", `text/plain; charset="utf-8"`)
		hdr("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, m.Text); err != nil {
			return "", nil, err
		}
		return from.Address, b.Bytes(), nil
	}
	sep := make([]byte, 12)
	if _, err := rand.Read(sep); err != nil {
		return "", nil, err
	}
	boundary := "st-" + hex.EncodeToString(sep)
	hdr("Content-Type", fmt.Sprintf(`multipart/alternative; boundary="%s"`, boundary))
	b.WriteString("\r\n")
	for _, part := range []struct{ typ, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=\"utf-8\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", boundary, part.typ)
		if err := writeQP(&b, part.body); err != nil {
			return "", nil, err
		}
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return from.Address, b.Bytes(), nil
}

func writeQP(b *bytes.Buffer, s string) error {
	w := quotedprintable.NewWriter(b)
	// Normalize line endings: the writer turns \n into \r\n, a bare \r would be encoded.
	if _, err := w.Write([]byte(strings.ReplaceAll(s, "\r\n", "\n"))); err != nil {
		return err
	}
	return w.Close()
}

var errNoSigner = errors.New("mailer: no DKIM key")
