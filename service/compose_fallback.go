package service

import "regexp"

// fallbackRef is ${NAME:-default}, ${NAME-default}, ${NAME:?message} or ${NAME?message}.
var fallbackRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*):?[-?]`)

// variablesWithFallback names the variables the text gives a default or a message for.
func variablesWithFallback(yaml []byte) map[string]bool {
	names := map[string]bool{}
	for _, m := range fallbackRef.FindAllSubmatch(yaml, -1) {
		names[string(m[1])] = true
	}
	return names
}
