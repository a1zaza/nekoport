package mail

import (
	"bytes"
	"github.com/a1zaza/nekoport/internal/domain"
	"strings"
	"testing"
)

func TestMIMERoundTripUnicodeAndBlindRecipients(t *testing.T) {
	a := domain.Account{Name: "测试发件人", Address: "sender@example.test"}
	d := domain.Draft{To: "接收者 <to@example.test>", BCC: "hidden@example.test", Subject: "中文主题", Attachments: []domain.Attachment{{ID: "attachment", Name: "中文附件.txt"}}}
	raw, rcpts, e := Build(a, d, "正文\n第二行", func(string) ([]byte, error) { return []byte("附件内容"), nil })
	if e != nil {
		t.Fatal(e)
	}
	if len(rcpts) != 2 || bytes.Contains(raw, []byte("hidden@example.test")) {
		t.Fatal("bcc leaked or envelope missing")
	}
	objects := map[string][]byte{}
	content, e := Parse(raw, func(b []byte) (string, error) { objects["saved"] = b; return "saved", nil })
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(content.Text, "正文") || len(content.Attachments) != 1 || content.Attachments[0].Name != "中文附件.txt" || string(objects["saved"]) != "附件内容" {
		t.Fatalf("%+v", content)
	}
}
func TestHeaderInjectionRejected(t *testing.T) {
	_, _, e := Build(domain.Account{Address: "a@example.test"}, domain.Draft{To: "b@example.test", Subject: "x\r\nBcc: victim@example.test"}, "", nil)
	if e == nil {
		t.Fatal("header injection allowed")
	}
}
func TestSanitization(t *testing.T) {
	clean := Sanitize(`<p>你好</p><script>alert(1)</script><img src="x" onerror="alert(1)"><iframe src="https://evil.test"></iframe>`)
	if strings.Contains(clean, "<script") || strings.Contains(clean, "onerror") || strings.Contains(clean, "<iframe") {
		t.Fatal(clean)
	}
	if !strings.Contains(Plain(clean), "你好") {
		t.Fatal(clean)
	}
}
func TestRejectPlaintextAndInvalidEndpoint(t *testing.T) {
	if ValidateEndpoint(domain.Endpoint{Host: "localhost", Port: 143, Username: "u", TLS: "none"}) == nil {
		t.Fatal("plaintext allowed")
	}
	if ValidateEndpoint(domain.Endpoint{Host: "mail.example.test", Port: 993, Username: "u", TLS: "tls"}) != nil {
		t.Fatal("valid rejected")
	}
}

func TestTooManyPartsCannotSilentlyTruncateForward(t *testing.T) {
	d := domain.Draft{To: "to@example.test", Subject: "分片上限"}
	for n := 0; n < 201; n++ {
		d.Attachments = append(d.Attachments, domain.Attachment{ID: "object", Name: "part.txt"})
	}
	raw, _, e := Build(domain.Account{Address: "from@example.test"}, d, "正文", func(string) ([]byte, error) { return []byte("data"), nil })
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Parse(raw, func([]byte) (string, error) { return "id", nil }); e == nil {
		t.Fatal("multipart content was silently truncated")
	}
}

func TestSafeMailStyles(t *testing.T) {
	clean := Sanitize(`<style>@import url(https://evil.test); .card { padding:24px; color:#345; background-image:url(https://evil.test/pixel) } @media (max-width:600px){.card{width:100%}} </style><table class="card" style="font-size:18px; padding:12px; background-image:url(https://evil.test); position:fixed"><tr><td bgcolor="#eee">正文</td></tr></table>`)
	for _, want := range []string{"<style>", ".card", "padding", "font-size", "@media", `class="card"`} {
		if !strings.Contains(clean, want) {
			t.Errorf("missing %s: %s", want, clean)
		}
	}
	for _, bad := range []string{"@import", "evil.test", "position", "background-image"} {
		if strings.Contains(clean, bad) {
			t.Errorf("unsafe %s: %s", bad, clean)
		}
	}
}

func TestPreserveMailRootColors(t *testing.T) {
	clean := Sanitize(`<html style="background-color:#f0f0f0"><body bgcolor="#fdf6e3" text="#123456" style="background-color:#fdf6e3;color:#123456"><p>正文</p></body></html>`)
	for _, want := range []string{"<html", "<body", `bgcolor="#fdf6e3"`, `text="#123456"`, "background-color", "#fdf6e3"} {
		if !strings.Contains(clean, want) {
			t.Errorf("lost root color %s: %s", want, clean)
		}
	}
}

func TestMailLayoutCompatibility(t *testing.T) {
	clean := Sanitize(`<style>.layout{display:grid;grid-template-columns:[start] 1fr 2fr;gap:16px;position:relative;overflow:hidden;box-shadow:0 2px 4px #0003;font-family:'霞鹜臻楷',sans-serif}@supports (display:grid){.layout{align-items:center}}.child{position:absolute;top:8px;transform:translateX(2px);opacity:.8}</style><main><table cellpadding="12" cellspacing="0"><tr><td valign="top" nowrap>正文</td></tr></table><div style="display:flex;flex-wrap:wrap;gap:12px;font-family:'霞鹜臻楷';position:relative">内容</div></main>`)
	for _, want := range []string{"grid-template-columns", "gap", "relative", "overflow", "box-shadow", "霞鹜臻楷", "@supports", "absolute", "transform", "opacity", "flex-wrap", `cellpadding="12"`, `valign="top"`, "<main>"} {
		if !strings.Contains(clean, want) {
			t.Errorf("lost layout %s: %s", want, clean)
		}
	}
	clean = Sanitize(`<style>.bad{position:fixed;background:url(https://tracker.test);font-family:var(--font);behavior:expression(alert(1))}</style><div style="position:fixed;background:url(https://tracker.test);font-family:var(--font)" onclick="alert(1)">正文</div><script>alert(1)</script>`)
	for _, bad := range []string{"fixed", "tracker.test", "var(", "expression", "onclick", "<script"} {
		if strings.Contains(clean, bad) {
			t.Errorf("unsafe %s: %s", bad, clean)
		}
	}
}
