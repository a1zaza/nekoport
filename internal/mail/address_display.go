package mail

import (
	"mime"
	"strings"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/emersion/go-message/charset"
	message "github.com/emersion/go-message/mail"
)

// Presentation values are derived at the API boundary. Keep the RFC 5322 source
// intact for address parsing, replies and SMTP envelopes; Address.String would
// quote names and encode non-ASCII names again, so do not use it for rendering.
func decodeDisplayHeader(value string) string {
	decoded, err := (&mime.WordDecoder{CharsetReader: charset.Reader}).DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}
func addressDisplay(value string) (full, names string) {
	addresses, err := message.ParseAddressList(value)
	if err != nil || len(addresses) == 0 {
		decoded := decodeDisplayHeader(value)
		return decoded, decoded
	}
	labels := make([]string, 0, len(addresses))
	short := make([]string, 0, len(addresses))
	for _, address := range addresses {
		name := strings.TrimSpace(address.Name)
		if name == "" {
			labels = append(labels, address.Address)
			short = append(short, address.Address)
		} else {
			labels = append(labels, name+" <"+address.Address+">")
			short = append(short, name)
		}
	}
	return strings.Join(labels, ", "), strings.Join(short, ", ")
}
func AddressDisplay(value string) string { full, _ := addressDisplay(value); return full }
func MailboxDisplay(name, address string) string {
	if parsed, err := message.ParseAddress(address); err == nil {
		address = parsed.Address
	}
	name = strings.TrimSpace(decodeDisplayHeader(name))
	if name == "" {
		return address
	}
	return name + " <" + address + ">"
}
func MessageDisplay(m domain.Message) domain.Message {
	m.FromDisplay, m.FromName = addressDisplay(m.From)
	m.ToDisplay = AddressDisplay(m.To)
	m.CCDisplay = AddressDisplay(m.CC)
	return m
}
