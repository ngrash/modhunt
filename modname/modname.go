package modname

import "strings"

func Canonicalize(s string) string {
	s = strings.ToLower(s)

	if strings.HasPrefix(s, "www.github.com/") {
		return strings.TrimPrefix(s, "www.")
	}

	return s
}
