package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/textproto"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ConnectionError preserves the failing stage without exposing authentication data.
type ConnectionError struct {
	Protocol  string
	Stage     string
	Retryable bool
	message   string
}

func (e *ConnectionError) Error() string { return e.message }
func connectionError(protocol, stage string, cause error, username, password string) error {
	if cause == nil {
		return nil
	}
	var netErr net.Error
	retryable := errors.As(cause, &netErr) || errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF) || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)
	var status *textproto.Error
	if errors.As(cause, &status) && status.Code >= 400 && status.Code < 500 {
		retryable = true
	}
	label := protocol + " " + stage + "失败"
	if stage == "认证" {
		if retryable {
			label = protocol + " 认证阶段连接中断或暂不可用"
		} else {
			label = protocol + " 认证失败（用户名、密码或账号策略被拒绝）"
		}
	}
	return &ConnectionError{Protocol: protocol, Stage: stage, Retryable: retryable, message: label + "：" + redactReply(cause.Error(), username, password)}
}
func redactReply(message, username, password string) string {
	values := []string{username, password}
	for _, v := range []string{username, password} {
		if v != "" {
			values = append(values, base64.StdEncoding.EncodeToString([]byte(v)), url.QueryEscape(v))
			q := strconv.Quote(v)
			values = append(values, q[1:len(q)-1])
		}
	}
	if username != "" || password != "" {
		values = append(values, base64.StdEncoding.EncodeToString([]byte("\x00"+username+"\x00"+password)))
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		if v != "" {
			message = strings.ReplaceAll(message, v, "[已隐藏]")
		}
	}
	message = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	runes := []rune(message)
	if len(runes) > 320 {
		message = string(runes[:320]) + "…"
	}
	if message == "" {
		message = "服务器未提供详细原因"
	}
	return message
}
