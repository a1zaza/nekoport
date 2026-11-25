package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
)

func TestConnectionErrorsPreserveStageAndHideCredentials(t *testing.T) {
	username := "private-user@example.test"
	password := "private-secret+value"
	encoded := base64.StdEncoding.EncodeToString([]byte("\x00" + username + "\x00" + password))
	reply := &textproto.Error{Code: 535, Msg: "authentication rejected " + username + " " + password + " " + base64.StdEncoding.EncodeToString([]byte(password)) + " " + url.QueryEscape(password) + " " + encoded}
	err := connectionError("SMTP", "认证", reply, username, password)
	var detail *ConnectionError
	if !errors.As(err, &detail) || detail.Protocol != "SMTP" || detail.Stage != "认证" || detail.Retryable {
		t.Fatal("lost failure stage or misclassified rejection")
	}
	for _, value := range []string{username, password, base64.StdEncoding.EncodeToString([]byte(password)), url.QueryEscape(password), encoded} {
		if strings.Contains(err.Error(), value) {
			t.Fatal("credentials leaked in error")
		}
	}
	if !strings.Contains(err.Error(), "535") || !strings.Contains(err.Error(), "账号策略") {
		t.Fatal("server status or contextual explanation lost")
	}
}
func TestAuthenticationTransportFailureIsNotPasswordRejection(t *testing.T) {
	err := connectionError("IMAP", "认证", context.DeadlineExceeded, "user", "secret")
	var detail *ConnectionError
	if !errors.As(err, &detail) || !detail.Retryable || strings.Contains(err.Error(), "认证失败") {
		t.Fatal("timeout mislabeled as wrong credentials")
	}
	tlsErr := connectionError("IMAP", "TLS 握手", errors.New("certificate verify failed"), "user", "secret")
	if !strings.Contains(tlsErr.Error(), "TLS 握手失败") || strings.Contains(tlsErr.Error(), "认证失败") {
		t.Fatal("TLS failure mislabeled as authentication")
	}
	temporary := connectionError("SMTP", "认证", &textproto.Error{Code: 454, Msg: "Temporary authentication failure"}, "user", "secret")
	if !errors.As(temporary, &detail) || !detail.Retryable || !strings.Contains(temporary.Error(), "454") {
		t.Fatal("temporary server failure mislabeled as password rejection")
	}
}
