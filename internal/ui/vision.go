package ui

import (
	"regexp"
	"strings"
)

func matchGlobList(list, id, display string) bool {
	for _, pat := range strings.Split(list, ",") {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if wildMatch(pat, id) || wildMatch(pat, display) {
			return true
		}
	}
	return false
}

func wildMatch(pat, s string) bool {
	if pat == "" || s == "" {
		return false
	}
	re := "(?i)^" + regexp.QuoteMeta(pat) + "$"
	re = strings.ReplaceAll(re, `\*`, `.*`)
	re = strings.ReplaceAll(re, `\?`, `.`)
	ok, err := regexp.MatchString(re, s)
	return err == nil && ok
}
