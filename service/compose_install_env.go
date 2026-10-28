package service

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// A compose text installed together with its .env: the dashboard sends the .env as the
// top-level `x-casaos-env` string of the compose body. It is taken out before anything loads
// the text, so it never reaches the compose file, and written as the app's .env instead.

const composeInstallEnvKey = "x-casaos-env"

// SplitInstallEnv takes the .env out of compose text (x-casaos-env) and returns the text
// without it. Text without the key comes back unchanged, byte for byte.
func SplitInstallEnv(text []byte) ([]byte, []byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return text, nil, nil
	}

	top := doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != composeInstallEnvKey {
			continue
		}
		value := top.Content[i+1]
		if value.Kind != yaml.ScalarNode {
			return nil, nil, fmt.Errorf("%s must be the text of a .env file", composeInstallEnvKey)
		}
		top.Content = append(top.Content[:i], top.Content[i+2:]...)
		out, err := yaml.Marshal(&doc)
		if err != nil {
			return nil, nil, err
		}
		return out, []byte(value.Value), nil
	}

	return text, nil, nil
}

// undefinedVolume is what compose says of a volume whose source starts with a variable that has
// no value: "$KEY/path" is then taken for the name of a volume.
var undefinedVolume = regexp.MustCompile(`undefined volume \$\{?([A-Za-z_][A-Za-z0-9_]*)`)

// withUndefinedVariableHint says what is wrong when a volume path starts with a variable the
// .env does not define, which compose words as an undefined volume.
func withUndefinedVariableHint(err error) error {
	if m := undefinedVolume.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("%w (the variable %s has no value: define it in the .env)", err, m[1])
	}
	return err
}

// NewComposeAppWithEnv loads compose text installed with its .env, twice. resolved takes the
// .env's values, an `env_file: .env` included: it is what the checks before the install look
// at (the ports). kept leaves every reference to a key of the .env as written: it is what the
// install writes beside the .env, so that the app's .env settings go on applying, as for an
// app whose .env was added later (see EnvKeys). A key CasaOS sets itself is refused, as there.
// COMPOSE_PROFILES in the .env turns profiles on, as docker compose reads it: a service of a
// profile it names is installed, and runs, since the runtime reads the same .env; a service of
// another profile is left out of the app.
func NewComposeAppWithEnv(text, env []byte) (resolved, kept *ComposeApp, err error) {
	values, err := ParseEnvFile(env)
	if err != nil {
		return nil, nil, fmt.Errorf(".env: %w", err)
	}

	dir, err := os.MkdirTemp("", "casaos-compose-app-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), env, 0o600); err != nil {
		return nil, nil, err
	}

	var profiles []string
	for _, p := range strings.Split(values["COMPOSE_PROFILES"], ",") {
		if p = strings.TrimSpace(p); p != "" {
			profiles = append(profiles, p)
		}
	}

	if resolved, err = newComposeAppFromYAML(text, false, true, nil, dir, values, profiles); err != nil {
		return nil, nil, withUndefinedVariableHint(err)
	}

	keep := map[string]struct{}{}
	for key := range values {
		keep[key] = struct{}{}
	}
	// the settings parse (ComposeAppFromSettingsYAML), with the profiles
	if kept, err = newComposeAppFromYAML(text, false, true, keep, "", nil, profiles); err != nil {
		return nil, nil, err
	}

	return resolved, kept, nil
}
