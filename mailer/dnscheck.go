package mailer

import (
	"context"
	"crypto"
	"fmt"
	"net"
	"strings"
)

// DNSExpect is what a server expects to find published.
type DNSExpect struct {
	Domain   string
	Hostname string
	Selector string
	Key      crypto.Signer
}

// CheckDNS checks the records receivers will look at. It reports, and changes
// nothing; a failure here often means mail lands in spam or bounces.
func CheckDNS(ctx context.Context, r *net.Resolver, e DNSExpect) []DNSCheck {
	if r == nil {
		r = net.DefaultResolver
	}
	var out []DNSCheck
	add := func(name string, ok bool, format string, a ...any) {
		out = append(out, DNSCheck{Name: name, OK: ok, Detail: fmt.Sprintf(format, a...)})
	}

	mx, err := r.LookupMX(ctx, e.Domain)
	ok := false
	var hosts []string
	for _, m := range mx {
		h := strings.TrimSuffix(m.Host, ".")
		hosts = append(hosts, h)
		ok = ok || strings.EqualFold(h, e.Hostname)
	}
	switch {
	case err != nil:
		add("MX "+e.Domain, false, "%v", err)
	case ok:
		add("MX "+e.Domain, true, "%s", strings.Join(hosts, ", "))
	default:
		add("MX "+e.Domain, false, "%s 不指向 %s", strings.Join(hosts, ", "), e.Hostname)
	}

	if ips, err := r.LookupIP(ctx, "ip4", e.Hostname); err != nil {
		add("A "+e.Hostname, false, "%v", err)
	} else {
		add("A "+e.Hostname, true, "%v", ips)
	}

	spfRec := ""
	txt, err := r.LookupTXT(ctx, e.Domain)
	for _, t := range txt {
		if strings.HasPrefix(strings.ToLower(t), "v=spf1") {
			spfRec = t
		}
	}
	switch {
	case err != nil:
		add("SPF "+e.Domain, false, "%v", err)
	case spfRec == "":
		add("SPF "+e.Domain, false, "没有 SPF 记录")
	default:
		add("SPF "+e.Domain, true, "%s", spfRec)
	}

	name := e.Selector + "._domainkey." + e.Domain
	want, _ := DKIMRecord(e.Key)
	txt, err = r.LookupTXT(ctx, name)
	found := strings.ReplaceAll(strings.Join(txt, ""), " ", "")
	switch {
	case err != nil:
		add("DKIM "+name, false, "%v", err)
	case found == strings.ReplaceAll(want, " ", ""):
		add("DKIM "+name, true, "与本机私钥一致")
	default:
		add("DKIM "+name, false, "记录与本机私钥不一致")
	}

	txt, err = r.LookupTXT(ctx, "_dmarc."+e.Domain)
	dmarc := strings.Join(txt, "")
	if err != nil || !strings.HasPrefix(strings.ToLower(dmarc), "v=dmarc1") {
		add("DMARC", false, "没有 DMARC 记录")
	} else {
		add("DMARC", true, "%s", dmarc)
	}
	return out
}
