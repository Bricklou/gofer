package message

import (
	"bufio"
	"bytes"
	"io"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// A SourceReport is what the headers of a message say about where it came
// from: the authentication results its receiving server recorded, the
// addresses it names, and anything among them that doesn't add up. It only
// reads what the headers claim; it checks no signature itself.
type SourceReport struct {
	Headers  []SourceHeader
	Checks   []SourceCheck
	Facts    []SourceFact
	Warnings []SourceWarning
	Hops     int
}

type SourceHeader struct {
	Name  string
	Value string
}

// SourceCheck is one authentication method and its result, such as SPF
// "pass", with what it was checked against.
type SourceCheck struct {
	Method string
	Result string
	Detail string
}

// Passed and Failed sort a result for display; anything else is neutral.
func (c SourceCheck) Passed() bool { return c.Result == "pass" }
func (c SourceCheck) Failed() bool {
	switch c.Result {
	case "fail", "softfail", "permerror", "temperror":
		return true
	}
	return false
}

type SourceFact struct {
	Label string
	Value string
}

// SourceWarning is a sign that the message may not be what it claims. Serious
// ones are what spam and phishing usually look like; the others are only
// worth knowing.
type SourceWarning struct {
	Text    string
	Serious bool
}

var authResultPattern = regexp.MustCompile(`(?i)\b(spf|dkim|dmarc|arc|bimi)\s*=\s*([a-z]+)([^;]*)`)
var tagPattern = regexp.MustCompile(`(?i)(?:^|;)\s*([a-z]+)\s*=\s*([^;]*)`)

// AnalyzeSource reads the header block of a raw message.
func AnalyzeSource(raw []byte) SourceReport {
	var report SourceReport
	header, err := textproto.NewReader(bufio.NewReader(io.MultiReader(bytes.NewReader(raw), strings.NewReader("\r\n\r\n")))).ReadMIMEHeader()
	if err != nil && len(header) == 0 {
		report.Warnings = append(report.Warnings, SourceWarning{Text: "The headers of this message couldn't be read."})
		return report
	}
	report.Headers = orderedHeaders(raw)
	report.Hops = len(header.Values("Received"))

	from := firstAddress(header.Get("From"))
	fromDomain := addressDomain(from)
	returnPath := strings.Trim(strings.TrimSpace(header.Get("Return-Path")), "<>")
	replyTo := firstAddress(header.Get("Reply-To"))

	addFact := func(label, value string) {
		if value = strings.TrimSpace(value); value != "" {
			report.Facts = append(report.Facts, SourceFact{Label: label, Value: value})
		}
	}
	addFact("From", DecodeHeader(header.Get("From")))
	addFact("Envelope sender", returnPath)
	addFact("Reply-To", DecodeHeader(header.Get("Reply-To")))
	addFact("Sender", DecodeHeader(header.Get("Sender")))
	addFact("Date", header.Get("Date"))
	addFact("Message-ID", header.Get("Message-ID"))
	addFact("Mailer", firstNonEmpty(header.Get("X-Mailer"), header.Get("User-Agent")))
	addFact("Spam score", firstNonEmpty(header.Get("X-Spam-Status"), header.Get("X-Spam-Score")))

	// The topmost results were recorded by the server that delivered the
	// message to this mailbox; older ones further down may be forged.
	var dkimDomains []string
	if results := header.Values("Authentication-Results"); len(results) > 0 {
		for _, match := range authResultPattern.FindAllStringSubmatch(results[0], -1) {
			check := SourceCheck{Method: strings.ToUpper(match[1]), Result: strings.ToLower(match[2]), Detail: strings.TrimSpace(match[3])}
			report.Checks = append(report.Checks, check)
			if check.Method == "DKIM" && check.Passed() {
				if d := propertyValue(check.Detail, "header.d", "header.i"); d != "" {
					dkimDomains = append(dkimDomains, strings.TrimPrefix(d, "@"))
				}
			}
		}
	}
	if !hasCheck(report.Checks, "SPF") {
		if spf := strings.TrimSpace(header.Get("Received-SPF")); spf != "" {
			result, detail, _ := strings.Cut(spf, " ")
			report.Checks = append(report.Checks, SourceCheck{Method: "SPF", Result: strings.ToLower(result), Detail: strings.TrimSpace(detail)})
		}
	}
	var signers []string
	for _, signature := range header.Values("DKIM-Signature") {
		tags := signatureTags(signature)
		if tags["d"] != "" {
			signers = append(signers, tags["d"]+" (selector "+tags["s"]+")")
		}
	}
	addFact("DKIM signed by", strings.Join(signers, ", "))

	warn := func(serious bool, text string) {
		report.Warnings = append(report.Warnings, SourceWarning{Text: text, Serious: serious})
	}
	for _, check := range report.Checks {
		if check.Failed() && (check.Method == "SPF" || check.Method == "DKIM" || check.Method == "DMARC") {
			warn(true, check.Method+" check result is \""+check.Result+"\": the sending server may not be allowed to send for this domain.")
		}
	}
	if len(report.Checks) == 0 {
		warn(false, "No authentication results were recorded by the receiving server.")
	}
	if from == "" {
		warn(true, "The message has no valid From address.")
	}
	if rpDomain := addressDomain(returnPath); fromDomain != "" && rpDomain != "" && !sameOrganization(fromDomain, rpDomain) {
		warn(false, "The envelope sender ("+rpDomain+") is a different domain than the From address ("+fromDomain+"). Mailing lists and sending services do this too.")
	}
	if replyDomain := addressDomain(replyTo); fromDomain != "" && replyDomain != "" && !sameOrganization(fromDomain, replyDomain) {
		warn(true, "Replies go to a different domain ("+replyDomain+") than the one the message is from ("+fromDomain+").")
	}
	if fromDomain != "" && len(dkimDomains) > 0 && !anyAligned(fromDomain, dkimDomains) {
		warn(false, "The DKIM signature is from "+strings.Join(dkimDomains, ", ")+", not from the domain of the From address ("+fromDomain+").")
	}
	if name := fromDisplayName(header.Get("From")); name != "" {
		if shown := firstAddress(name); shown != "" && !strings.EqualFold(shown, from) {
			warn(true, "The sender's name shows the address "+shown+", but the message is really from "+from+".")
		}
	}
	if header.Get("Message-ID") == "" {
		warn(false, "The message has no Message-ID.")
	}
	if date, err := mail.ParseDate(header.Get("Date")); err == nil && date.After(time.Now().Add(24*time.Hour)) {
		warn(false, "The message is dated in the future.")
	}
	if strings.EqualFold(strings.TrimSpace(header.Get("X-Spam-Flag")), "yes") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(header.Get("X-Spam-Status"))), "yes") {
		warn(true, "The mail server marked this message as spam.")
	}
	return report
}

// orderedHeaders lists the headers as they appear, unfolded, without the
// reordering a header map does.
func orderedHeaders(raw []byte) []SourceHeader {
	var headers []SourceHeader
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if line == "" {
			break
		}
		if (line[0] == ' ' || line[0] == '\t') && len(headers) > 0 {
			headers[len(headers)-1].Value += " " + strings.TrimSpace(line)
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		headers = append(headers, SourceHeader{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	return headers
}

func firstAddress(value string) string {
	addresses, err := mail.ParseAddressList(DecodeHeader(value))
	if err != nil || len(addresses) == 0 {
		return ""
	}
	return strings.ToLower(addresses[0].Address)
}

func fromDisplayName(value string) string {
	address, err := mail.ParseAddress(DecodeHeader(value))
	if err != nil {
		return ""
	}
	return address.Name
}

func addressDomain(address string) string {
	_, domain, ok := strings.Cut(address, "@")
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(domain))
}

// sameOrganization compares registered domains, so mail.example.com and
// example.com count as one sender.
func sameOrganization(a, b string) bool {
	orgA, errA := publicsuffix.EffectiveTLDPlusOne(a)
	orgB, errB := publicsuffix.EffectiveTLDPlusOne(b)
	if errA != nil || errB != nil {
		return strings.EqualFold(a, b)
	}
	return strings.EqualFold(orgA, orgB)
}

func anyAligned(domain string, others []string) bool {
	for _, other := range others {
		if sameOrganization(domain, addressDomain("x@"+other)) {
			return true
		}
	}
	return false
}

func hasCheck(checks []SourceCheck, method string) bool {
	for _, check := range checks {
		if check.Method == method {
			return true
		}
	}
	return false
}

// propertyValue finds a property such as header.d=example.com in the detail
// of an authentication result.
func propertyValue(detail string, names ...string) string {
	for _, field := range strings.Fields(detail) {
		for _, name := range names {
			if value, ok := strings.CutPrefix(strings.ToLower(field), name+"="); ok {
				return strings.Trim(value, "\"();")
			}
		}
	}
	return ""
}

func signatureTags(signature string) map[string]string {
	tags := make(map[string]string)
	for _, match := range tagPattern.FindAllStringSubmatch(signature, -1) {
		tags[strings.ToLower(match[1])] = strings.Join(strings.Fields(match[2]), "")
	}
	return tags
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
