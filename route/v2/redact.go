package v2

import (
	"bytes"
	"maps"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/types"
)

// Environment values, build arguments, inline configs and secrets, and a container's command
// are the owner's: passwords and tokens live there. Every other caller gets the names with
// empty values, so a screen can still say what is set. A handler decides once, with
// isOwner, and passes owner to each of these.

// redactEnv empties the values unless owner; the map given is never changed.
func redactEnv(owner bool, env map[string]*string) map[string]*string {
	if env == nil || owner {
		return env
	}
	out := make(map[string]*string, len(env))
	for name := range env {
		out[name] = new(string)
	}

	return out
}

// blankValues is the map with its names and empty values; the map given is never changed.
func blankValues[M ~map[string]string](m M) M {
	if m == nil {
		return nil
	}
	out := make(M, len(m))
	for name := range m {
		out[name] = ""
	}

	return out
}

// redactProject is the project with each service's environment and build arguments through
// redactEnv, its command, entrypoint and health check emptied, the values of labels and driver
// options emptied, and the content of inline configs and secrets emptied. What it changes is
// copied, never edited: the project may be the one other callers hold.
func redactProject(owner bool, project *types.Project) *types.Project {
	if project == nil || owner {
		return project
	}
	out := *project
	out.Services = make(types.Services, len(project.Services))
	for name, service := range project.Services {
		service.Environment = redactEnv(owner, service.Environment)
		if service.Build != nil {
			build := *service.Build
			build.Args = redactEnv(owner, build.Args)
			service.Build = &build
		}
		// compose fills these from .env too: the command and its checks say no word to a user
		service.Command, service.Entrypoint = nil, nil
		if service.HealthCheck != nil {
			check := *service.HealthCheck
			check.Test = nil
			service.HealthCheck = &check
		}
		service.Labels = blankValues(service.Labels)
		if service.Logging != nil {
			logging := *service.Logging
			logging.Options = blankValues(logging.Options)
			service.Logging = &logging
		}
		out.Services[name] = service
	}
	if project.Volumes != nil {
		out.Volumes = make(types.Volumes, len(project.Volumes))
		for name, volume := range project.Volumes {
			volume.DriverOpts, volume.Labels = blankValues(volume.DriverOpts), blankValues(volume.Labels)
			out.Volumes[name] = volume
		}
	}
	if project.Networks != nil {
		out.Networks = make(types.Networks, len(project.Networks))
		for name, network := range project.Networks {
			network.DriverOpts, network.Labels = blankValues(network.DriverOpts), blankValues(network.Labels)
			out.Networks[name] = network
		}
	}
	if project.Configs != nil {
		out.Configs = make(types.Configs, len(project.Configs))
		for name, config := range project.Configs {
			config.Content = ""
			out.Configs[name] = config
		}
	}
	if project.Secrets != nil {
		out.Secrets = make(types.Secrets, len(project.Secrets))
		for name, secret := range project.Secrets {
			secret.Content = ""
			out.Secrets[name] = secret
		}
	}

	return &out
}

// RedactEnvList is redactEnv for Docker's `NAME=value` list.
func RedactEnvList(owner bool, env []string) []string {
	if owner {
		return env
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		out = append(out, name+"=")
	}

	return out
}

// redactEnvText is redactEnv for a `.env` file: one `NAME=` line per variable, sorted. The
// names come from the parser compose uses, so a value spread over lines is never taken for a
// name; text it cannot read gives nothing.
func redactEnvText(owner bool, text []byte) []byte {
	if owner {
		return text
	}
	env, err := dotenv.ParseWithLookup(bytes.NewReader(text), func(string) (string, bool) { return "", false })
	if err != nil {
		return nil
	}
	var out strings.Builder
	for _, name := range slices.Sorted(maps.Keys(env)) {
		out.WriteString(name + "=\n")
	}

	return []byte(out.String())
}
