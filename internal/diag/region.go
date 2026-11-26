package diag

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// RegionEnv overrides the country the system settings give (two letters, such
// as DE); for tests, and for people whose settings do not say where they are.
const RegionEnv = "SHARETOKEN_REGION"

// eea are the countries of the European Economic Area: the EU and Iceland,
// Liechtenstein and Norway, where the GDPR applies. Reports start off there.
var eea = map[string]bool{
	"AT": true, "BE": true, "BG": true, "HR": true, "CY": true, "CZ": true, "DK": true, "EE": true, "FI": true, "FR": true,
	"DE": true, "GR": true, "HU": true, "IE": true, "IT": true, "LV": true, "LT": true, "LU": true, "MT": true, "NL": true,
	"PL": true, "PT": true, "RO": true, "SK": true, "SI": true, "ES": true, "SE": true, "IS": true, "LI": true, "NO": true,
	"AX": true, // Åland, part of Finland
}

// eeaZones are the IANA time zones of those countries.
var eeaZones = map[string]bool{
	"Europe/Vienna": true, "Europe/Brussels": true, "Europe/Sofia": true, "Europe/Zagreb": true, "Asia/Nicosia": true,
	"Asia/Famagusta": true, "Europe/Nicosia": true, "Europe/Prague": true, "Europe/Copenhagen": true, "Europe/Tallinn": true,
	"Europe/Helsinki": true, "Europe/Mariehamn": true, "Europe/Paris": true, "Europe/Berlin": true, "Europe/Busingen": true,
	"Europe/Athens": true, "Europe/Budapest": true, "Europe/Dublin": true, "Europe/Rome": true, "Europe/Riga": true,
	"Europe/Vilnius": true, "Europe/Luxembourg": true, "Europe/Malta": true, "Europe/Amsterdam": true, "Europe/Warsaw": true,
	"Europe/Lisbon": true, "Atlantic/Madeira": true, "Atlantic/Azores": true, "Europe/Bucharest": true, "Europe/Bratislava": true,
	"Europe/Ljubljana": true, "Europe/Madrid": true, "Africa/Ceuta": true, "Atlantic/Canary": true, "Europe/Stockholm": true,
	"Atlantic/Reykjavik": true, "Europe/Vaduz": true, "Europe/Oslo": true, "Arctic/Longyearbyen": true,
}

// Region is what the system says about where its user is.
type Region struct {
	Country string `json:"country,omitempty"` // from the language and region settings, or $SHARETOKEN_REGION
	Zone    string `json:"zone,omitempty"`    // the IANA time zone
	EEA     bool   `json:"eea"`               // either one is in the European Economic Area
}

var (
	regionMu  sync.Mutex
	regionVal *Region
)

// SystemRegion is this machine's Region, found once.
func SystemRegion() Region {
	regionMu.Lock()
	defer regionMu.Unlock()
	if regionVal == nil {
		r := detectRegion()
		regionVal = &r
	}
	return *regionVal
}

// ResetRegion forgets the Region found, so the next call looks again (tests).
func ResetRegion() {
	regionMu.Lock()
	regionVal = nil
	regionMu.Unlock()
}

func detectRegion() Region {
	if c := strings.ToUpper(strings.TrimSpace(os.Getenv(RegionEnv))); c != "" {
		return Region{Country: c, EEA: eea[c]}
	}
	r := Region{Country: systemCountry(), Zone: systemZone()}
	r.EEA = eea[r.Country] || eeaZones[r.Zone]
	return r
}

// DefaultUpload is whether reports are on for someone who did not choose: on,
// except in the European Economic Area.
func DefaultUpload() bool { return !SystemRegion().EEA }

// countryOfLocale reads the country of a locale name: de_DE.UTF-8, en-GB,
// zh_Hans_CN, sr_RS@latin.
func countryOfLocale(l string) string {
	if i := strings.IndexAny(l, ".@"); i >= 0 {
		l = l[:i]
	}
	parts := strings.FieldsFunc(l, func(r rune) bool { return r == '_' || r == '-' })
	for i := len(parts) - 1; i >= 1; i-- {
		if p := parts[i]; len(p) == 2 && isAlpha(p) {
			return strings.ToUpper(p)
		}
	}
	return ""
}

func isAlpha(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func envCountry() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if c := countryOfLocale(os.Getenv(k)); c != "" {
			return c
		}
	}
	return ""
}

// zoneOfLink reads a zone name from the target of /etc/localtime:
// /usr/share/zoneinfo/Europe/Berlin, /var/db/timezone/zoneinfo/Asia/Shanghai.
func zoneOfLink(target string) string {
	target = filepath.ToSlash(target)
	if i := strings.Index(target, "zoneinfo/"); i >= 0 {
		return target[i+len("zoneinfo/"):]
	}
	return ""
}
