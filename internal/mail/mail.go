package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	_ "github.com/emersion/go-message/charset"
	message "github.com/emersion/go-message/mail"
	"github.com/microcosm-cc/bluemonday"
	"golang.org/x/net/html"
)

const MaxMIME = 30 << 20
const MaxAttachment = 20 << 20

type watchedConn struct {
	net.Conn
	stop func() bool
}

func (c *watchedConn) Close() error { c.stop(); return c.Conn.Close() }
func watch(ctx context.Context, c net.Conn) net.Conn {
	return &watchedConn{c, context.AfterFunc(ctx, func() { c.Close() })}
}

func ValidateEndpoint(e domain.Endpoint) error {
	if strings.TrimSpace(e.Host) == "" || strings.ContainsAny(e.Host, "/\\\r\n\t ") || strings.ContainsAny(e.Username, "\r\n") || e.Username == "" || e.Port < 1 || e.Port > 65535 {
		return errors.New("请填写有效的服务器、端口和登录用户名")
	}
	if e.TLS != "tls" && e.TLS != "starttls" {
		return errors.New("仅支持 TLS 或严格 STARTTLS")
	}
	return nil
}
func DialIMAP(ctx context.Context, e domain.Endpoint, password string) (*client.Client, error) {
	if err := ValidateEndpoint(e); err != nil {
		return nil, err
	}
	fail := func(stage string, err error) error { return connectionError("IMAP", stage, err, e.Username, password) }
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(e.Host, fmt.Sprint(e.Port)))
	if err != nil {
		return nil, fail("连接", err)
	}
	conn = watch(ctx, conn)
	conn.SetDeadline(time.Now().Add(45 * time.Second))
	cfg := &tls.Config{ServerName: e.Host, MinVersion: tls.VersionTLS12}
	if e.TLS == "tls" {
		tc := tls.Client(conn, cfg)
		if err = tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fail("TLS 握手", err)
		}
		conn = tc
	}
	c, err := client.New(conn)
	if err != nil {
		conn.Close()
		return nil, fail("协议握手", err)
	}
	// Library reader logs can contain untrusted server replies. Surface only redacted errors.
	c.ErrorLog = log.New(io.Discard, "", 0)
	c.Timeout = 45 * time.Second
	if e.TLS == "starttls" {
		if err = c.StartTLS(cfg); err != nil {
			c.Terminate()
			return nil, fail("STARTTLS 升级", err)
		}
	}
	if err = c.Login(e.Username, password); err != nil {
		c.Terminate()
		return nil, fail("认证", err)
	}
	return c, nil
}
func DialSMTP(ctx context.Context, e domain.Endpoint, password string) (*smtp.Client, error) {
	if err := ValidateEndpoint(e); err != nil {
		return nil, err
	}
	fail := func(stage string, err error) error { return connectionError("SMTP", stage, err, e.Username, password) }
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(e.Host, fmt.Sprint(e.Port)))
	if err != nil {
		return nil, fail("连接", err)
	}
	conn = watch(ctx, conn)
	conn.SetDeadline(time.Now().Add(60 * time.Second))
	cfg := &tls.Config{ServerName: e.Host, MinVersion: tls.VersionTLS12}
	if e.TLS == "tls" {
		tc := tls.Client(conn, cfg)
		if err = tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fail("TLS 握手", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, e.Host)
	if err != nil {
		conn.Close()
		return nil, fail("协议握手", err)
	}
	if err = c.Hello("localhost"); err != nil {
		c.Close()
		return nil, fail("EHLO 握手", err)
	}
	if e.TLS == "starttls" {
		if err = c.StartTLS(cfg); err != nil {
			c.Close()
			return nil, fail("STARTTLS 升级", err)
		}
	}
	supported, offered := c.Extension("AUTH")
	if !supported {
		c.Close()
		return nil, fail("认证方式", errors.New("服务器未提供 AUTH，请核对 SMTP 服务器与端口"))
	}
	plain := false
	for _, method := range strings.Fields(offered) {
		if strings.EqualFold(method, "PLAIN") {
			plain = true
			break
		}
	}
	if !plain {
		c.Close()
		return nil, fail("认证方式", errors.New("服务器未提供 PLAIN 认证，请核对所支持的认证方式"))
	}
	if err = c.Auth(smtp.PlainAuth("", e.Username, password, e.Host)); err != nil {
		c.Close()
		return nil, fail("认证", err)
	}
	return c, nil
}
func Send(ctx context.Context, a domain.Account, password string, recipients []string, raw []byte, beforeData func() error) (unknown bool, err error) {
	c, e := DialSMTP(ctx, a.SMTP, password)
	if e != nil {
		return false, e
	}
	defer c.Close()
	return deliver(c, a.Address, recipients, raw, beforeData)
}
func deliver(c *smtp.Client, address string, recipients []string, raw []byte, beforeData func() error) (bool, error) {
	var e error
	addr, _ := stdmail.ParseAddress(address)
	if addr == nil {
		return false, errors.New("发件地址无效")
	}
	if e = c.Mail(addr.Address); e != nil {
		return false, e
	}
	for _, r := range recipients {
		if e = c.Rcpt(r); e != nil {
			c.Reset()
			return false, errors.New("部分收件人被拒收，本次未发送")
		}
	}
	if e = beforeData(); e != nil {
		return false, e
	}
	w, e := c.Data()
	if e != nil {
		return false, e
	}
	if _, e = w.Write(raw); e != nil {
		return true, e
	}
	if e = w.Close(); e != nil {
		return true, e
	}
	return false, nil
}
func Addresses(s string) ([]*stdmail.Address, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	return stdmail.ParseAddressList(s)
}
func Build(a domain.Account, d domain.Draft, body string, load func(string) ([]byte, error)) ([]byte, []string, error) {
	if strings.ContainsAny(d.Subject, "\r\n") || strings.ContainsAny(d.InReplyTo, "\r\n") {
		return nil, nil, errors.New("主题或引用头部不能包含换行")
	}
	from, e := stdmail.ParseAddress(a.Address)
	if e != nil {
		return nil, nil, e
	}
	from.Name = a.Name
	lists := make([][]*stdmail.Address, 3)
	for i, s := range []string{d.To, d.CC, d.BCC} {
		lists[i], e = Addresses(s)
		if e != nil {
			return nil, nil, errors.New("收件地址格式错误")
		}
	}
	var recipients []string
	seen := map[string]bool{}
	for _, ls := range lists {
		for _, addr := range ls {
			if !seen[addr.Address] {
				recipients = append(recipients, addr.Address)
				seen[addr.Address] = true
			}
		}
	}
	if len(recipients) == 0 {
		return nil, nil, errors.New("至少填写一个收件人")
	}
	join := func(as []*stdmail.Address) string {
		v := []string{}
		for _, a := range as {
			v = append(v, a.String())
		}
		return strings.Join(v, ", ")
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\n", from.String(), join(lists[0]))
	if len(lists[1]) > 0 {
		fmt.Fprintf(&b, "Cc: %s\r\n", join(lists[1]))
	}
	fmt.Fprintf(&b, "Subject: %s\r\nDate: %s\r\nMessage-ID: <%s@nekoport.local>\r\nMIME-Version: 1.0\r\n", mime.QEncoding.Encode("UTF-8", d.Subject), time.Now().Format(time.RFC1123Z), storage.ID())
	if d.InReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\nReferences: %s\r\n", d.InReplyTo, d.InReplyTo)
	}
	text := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	if len(d.Attachments) == 0 {
		b.WriteString("Content-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
		writeBase64(&b, []byte(text))
	} else {
		w := multipart.NewWriter(&b)
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", w.Boundary())
		h := make(map[string][]string)
		h["Content-Type"] = []string{"text/plain; charset=UTF-8"}
		h["Content-Transfer-Encoding"] = []string{"base64"}
		part, e := w.CreatePart(h)
		if e != nil {
			return nil, nil, e
		}
		writeBase64(part, []byte(text))
		for _, at := range d.Attachments {
			data, e := load(at.ID)
			if e != nil {
				return nil, nil, errors.New("附件内容缺失")
			}
			h = map[string][]string{"Content-Type": {mime.FormatMediaType("application/octet-stream", map[string]string{"name": at.Name})}, "Content-Disposition": {mime.FormatMediaType("attachment", map[string]string{"filename": at.Name})}, "Content-Transfer-Encoding": {"base64"}}
			part, e = w.CreatePart(h)
			if e != nil {
				return nil, nil, e
			}
			writeBase64(part, data)
		}
		if e = w.Close(); e != nil {
			return nil, nil, e
		}
	}
	if b.Len() > MaxMIME {
		return nil, nil, errors.New("邮件总大小超过 30 MiB")
	}
	return b.Bytes(), recipients, nil
}
func writeBase64(w io.Writer, b []byte) {
	s := base64.StdEncoding.EncodeToString(b)
	for len(s) > 76 {
		io.WriteString(w, s[:76]+"\r\n")
		s = s[76:]
	}
	io.WriteString(w, s+"\r\n")
}

const HTMLVersion = 3

func Parse(raw []byte, save func([]byte) (string, error)) (domain.Content, error) {
	out := domain.Content{HTMLVersion: HTMLVersion, Attachments: []domain.Attachment{}}
	r, e := message.CreateReader(bytes.NewReader(raw))
	if e != nil {
		return out, e
	}
	defer r.Close()
	var total int64
	for n := 0; n <= 200; n++ {
		p, e := r.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return out, e
		}
		if n == 200 {
			return out, errors.New("邮件分片超过 200 个，暂不解析")
		}
		b, e := io.ReadAll(io.LimitReader(p.Body, MaxAttachment+1))
		if e != nil {
			return out, e
		}
		total += int64(len(b))
		if len(b) > MaxAttachment || total > MaxMIME {
			return out, errors.New("邮件解码后超过大小限制")
		}
		switch h := p.Header.(type) {
		case *message.InlineHeader:
			typ, _, _ := h.ContentType()
			if typ == "text/html" {
				out.HTML = Sanitize(string(b))
			} else if typ == "text/plain" {
				out.Text = string(b)
			} else {
				id, e := save(b)
				if e != nil {
					return out, e
				}
				out.Attachments = append(out.Attachments, domain.Attachment{ID: id, Name: "内嵌资源", Type: typ, Size: int64(len(b)), CID: strings.Trim(h.Get("Content-ID"), "<>")})
			}
		case *message.AttachmentHeader:
			name, _ := h.Filename()
			if name == "" {
				name = "附件"
			}
			typ, _, _ := h.ContentType()
			id, e := save(b)
			if e != nil {
				return out, e
			}
			out.Attachments = append(out.Attachments, domain.Attachment{ID: id, Name: name, Type: typ, Size: int64(len(b)), CID: strings.Trim(h.Get("Content-ID"), "<>")})
		}
	}
	if out.Text == "" && out.HTML != "" {
		out.Text = Plain(out.HTML)
	}
	return out, nil
}
func Sanitize(s string) string {
	p := bluemonday.UGCPolicy()
	p.AllowURLSchemes("cid")
	p.AllowElements("html", "body", "header", "footer", "main")
	p.AllowNoAttrs().OnElements("main")
	p.AllowAttrs("align", "valign", "width", "height", "cellpadding", "cellspacing", "border", "nowrap").OnElements("table", "thead", "tbody", "tfoot", "tr", "td", "th", "col", "colgroup")
	p.AllowAttrs("text", "link", "alink", "vlink").OnElements("body")
	p.AllowAttrs("class", "id").Globally()
	p.AllowAttrs("bgcolor", "color").Globally()
	properties := strings.Fields(strings.ReplaceAll(mailStyleProperties, " position ", " "))
	p.AllowStyles(properties...).MatchingHandler(safeStyleValue).Globally()
	p.AllowStyles("position").MatchingHandler(safePosition).Globally()
	p.RequireNoFollowOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return mailStyles(s) + p.Sanitize(s)
}
func Plain(s string) string {
	r, e := html.Parse(strings.NewReader(s))
	if e != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && (n.Data == "p" || n.Data == "div" || n.Data == "br") {
			b.WriteByte('\n')
		}
	}
	walk(r)
	return strings.TrimSpace(b.String())
}
func Envelope(m *imap.Message, a domain.Account, box domain.Mailbox) domain.Message {
	v := domain.Message{ID: storage.ID(), AccountID: a.ID, MailboxID: box.ID, UID: m.Uid, UIDValidity: box.UIDValidity, Date: m.InternalDate, Size: m.Size, Revision: 1}
	if m.Envelope != nil {
		e := m.Envelope
		v.Subject = e.Subject
		v.MessageID = e.MessageId
		if !e.Date.IsZero() {
			v.Date = e.Date
		}
		format := func(as []*imap.Address) string {
			r := []string{}
			for _, a := range as {
				r = append(r, (&stdmail.Address{Name: a.PersonalName, Address: a.Address()}).String())
			}
			return strings.Join(r, ", ")
		}
		v.From = format(e.From)
		v.ReplyTo = format(e.ReplyTo)
		v.To = format(e.To)
		v.CC = format(e.Cc)
	}
	for _, f := range m.Flags {
		if f == imap.SeenFlag {
			v.Seen = true
		}
		if f == imap.FlaggedFlag {
			v.Starred = true
		}
	}
	v.HasAttachments = hasAttachments(m.BodyStructure)
	return v
}
func hasAttachments(b *imap.BodyStructure) bool {
	if b == nil {
		return false
	}
	if strings.EqualFold(b.Disposition, "attachment") {
		return true
	}
	for _, p := range b.Parts {
		if hasAttachments(p) {
			return true
		}
	}
	return false
}
