package mail

import (
	"mime"
	"testing"

	"github.com/a1zaza/nekoport/internal/domain"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestReadableAddressPresentation(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("张三"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, source, full, short string }{
		{"quoted", `"项目团队" <team@example.test>`, "项目团队 <team@example.test>", "项目团队"},
		{"utf8-base64", mime.BEncoding.Encode("UTF-8", "项目团队") + " <team@example.test>", "项目团队 <team@example.test>", "项目团队"},
		{"utf8-q", mime.QEncoding.Encode("UTF-8", "项目团队") + " <team@example.test>", "项目团队 <team@example.test>", "项目团队"},
		{"gbk", mime.BEncoding.Encode("GBK", string(gbk)) + " <zhang@example.test>", "张三 <zhang@example.test>", "张三"},
		{"bare", "team@example.test", "team@example.test", "team@example.test"},
		{"comma and embedded quote", `"Doe, Jane" <jane@example.test>, "Team \"Red\"" <red@example.test>`, `Doe, Jane <jane@example.test>, Team "Red" <red@example.test>`, `Doe, Jane, Team "Red"`},
		{"empty", "", "", ""},
		{"malformed", "unfinished <", "unfinished <", "unfinished <"},
		{"unsupported", `=?unknown?b?YWJj?= <team@example.test>`, `=?unknown?b?YWJj?= <team@example.test>`, `=?unknown?b?YWJj?= <team@example.test>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full, short := addressDisplay(tc.source)
			if full != tc.full || short != tc.short {
				t.Fatalf("full=%q, short=%q", full, short)
			}
		})
	}
}
func TestMessageViewDoesNotRewriteReplyAddresses(t *testing.T) {
	raw := mime.QEncoding.Encode("UTF-8", "项目团队") + " <team@example.test>"
	original := domain.Message{From: raw, ReplyTo: raw, To: `"Doe, Jane" <jane@example.test>`, CC: "copy@example.test"}
	view := MessageDisplay(original)
	if view.From != raw || view.ReplyTo != raw || original.FromName != "" {
		t.Fatal("source addresses were rewritten")
	}
	if view.FromName != "项目团队" || view.FromDisplay != "项目团队 <team@example.test>" || view.ToDisplay != "Doe, Jane <jane@example.test>" || view.CCDisplay != "copy@example.test" {
		t.Fatalf("bad display: %+v", view)
	}
}
