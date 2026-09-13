package service

import (
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Numbers written as strings.
//
// `target: "3000"`, `retries: "3"`: two apps in the catalogues this distribution
// ships -- PsiTransfer in IceWhale's, ayon in big-bear's -- quote a number the
// compose specification types as an integer, and compose-go's validation
// refuses the file. So the app was not in the store at all, and the log said
// "contact the contributor" every ten minutes to nobody. Docker itself is not
// this strict: a quoted number in a compose file is a number to everyone but
// the schema.
//
// Only fields the specification types as integers are touched, and only when
// the string is nothing but digits. A published port written as a range, a
// string a person meant as a string, anything else, is left exactly as it is.
// A file with nothing to coerce goes to the loader untouched, bytes and all,
// so the common case does not pass through a marshaller at all.

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

// coerceNumericStrings returns the YAML with digit-only strings turned into
// integers where the specification wants integers, or the input itself when
// there was nothing to turn.
func coerceNumericStrings(in []byte) []byte {
	var doc yaml.Node
	if err := yaml.Unmarshal(in, &doc); err != nil || len(doc.Content) == 0 {
		return in
	}

	changed := false
	services := childOf(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return in
	}
	for i := 1; i < len(services.Content); i += 2 {
		service := services.Content[i]
		if ports := childOf(service, "ports"); ports != nil && ports.Kind == yaml.SequenceNode {
			for _, port := range ports.Content {
				if port.Kind != yaml.MappingNode {
					continue
				}
				changed = coerce(childOf(port, "target")) || changed
				changed = coerce(childOf(port, "published")) || changed
			}
		}
		if healthcheck := childOf(service, "healthcheck"); healthcheck != nil {
			changed = coerce(childOf(healthcheck, "retries")) || changed
		}
		if deploy := childOf(service, "deploy"); deploy != nil {
			changed = coerce(childOf(deploy, "replicas")) || changed
		}
	}

	if !changed {
		return in
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return in
	}

	return out
}

// childOf finds the value under key in a mapping node, or nil.
func childOf(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}

	return nil
}

// coerce turns a digit-only quoted scalar into a plain integer scalar and says
// whether it did.
func coerce(node *yaml.Node) bool {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" || !digitsOnly.MatchString(node.Value) {
		return false
	}
	if _, err := strconv.Atoi(node.Value); err != nil {
		return false
	}

	node.Tag = "!!int"
	node.Style = 0

	return true
}
