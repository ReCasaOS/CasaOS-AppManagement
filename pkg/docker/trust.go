/*
credit: https://github.com/containrrr/watchtower
*/
package docker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/distribution/reference"
	cliconfig "github.com/docker/cli/cli/config"
	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/credentials"
	"github.com/docker/cli/cli/config/types"
)

// EncodedAuth returns an encoded auth config for the given registry
// loaded from environment variables or docker config
// as available in that order
func EncodedAuth(ref string) (string, error) {
	auth, err := EncodedEnvAuth(ref)
	if err != nil {
		auth, err = EncodedConfigAuth(ref)
	}
	return auth, err
}

// EncodedEnvAuth returns an encoded auth config for the given registry
// loaded from environment variables
// Returns an error if authentication environment variables have not been set
func EncodedEnvAuth(ref string) (string, error) {
	username := os.Getenv("REPO_USER")
	password := os.Getenv("REPO_PASS")
	if username != "" && password != "" {
		auth := types.AuthConfig{
			Username: username,
			Password: password,
		}
		return EncodeAuth(auth)
	}
	return "", errors.New("registry auth environment variables (REPO_USER, REPO_PASS) not set")
}

// EncodedConfigAuth returns an encoded auth config for the given registry
// loaded from the docker config
// Returns an empty string if credentials cannot be found for the referenced server
//
// The config is $DOCKER_CONFIG when set. Otherwise it is the one `docker login` writes
// (~/.docker/config.json, the home of the user this process runs as when it has no HOME), then
// the root of the file system, where a container that had the config mounted found it.
func EncodedConfigAuth(ref string) (string, error) {
	server, err := ParseServerAddress(ref)
	if err != nil {
		return "", err
	}
	dirs := []string{os.Getenv("DOCKER_CONFIG")}
	if dirs[0] == "" {
		dirs = []string{cliconfig.Dir(), "/"}
	}

	var firstErr error
	for _, dir := range dirs {
		configFile, err := cliconfig.Load(dir)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		credStore := CredentialsStore(*configFile)
		auth, _ := credStore.Get(server) // returns (types.AuthConfig{}) if server not in credStore

		if auth != (types.AuthConfig{}) {
			return EncodeAuth(auth)
		}
	}

	return "", firstErr
}

// ParseServerAddress extracts the server part from a container image ref
func ParseServerAddress(ref string) (string, error) {
	parsedRef, err := reference.Parse(ref)
	if err != nil {
		return ref, err
	}

	parts := strings.Split(parsedRef.String(), "/")
	return parts[0], nil
}

// CredentialsStore returns a new credentials store based
// on the settings provided in the configuration file.
func CredentialsStore(configFile configfile.ConfigFile) credentials.Store {
	if configFile.CredentialsStore != "" {
		return credentials.NewNativeStore(&configFile, configFile.CredentialsStore)
	}
	return credentials.NewFileStore(&configFile)
}

// EncodeAuth Base64 encode an AuthConfig struct for transmission over HTTP
func EncodeAuth(authConfig types.AuthConfig) (string, error) {
	buf, err := json.Marshal(authConfig)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(buf), nil
}
