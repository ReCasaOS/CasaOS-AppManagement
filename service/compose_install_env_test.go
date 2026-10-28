package service

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// a stack written for `docker compose --env-file`: anchors, an env_file, a variable the .env
// must give, and one with a default
const stackWithEnv = `name: vecto
x-php: &php
  image: ${IMAGE:-vecto}-php:latest
  env_file:
    - .env
  environment:
    DB_HOST: db
services:
  app:
    <<: *php
  queue:
    <<: *php
    deploy:
      replicas: ${FILES:-4}
  db:
    image: mariadb:11
    environment:
      MARIADB_PASSWORD: ${DB_PASSWORD:?DB_PASSWORD is missing}
    ports:
      - "${DB_PORT}:3306"
`

func TestTheEnvSentWithTheComposeIsTakenOut(t *testing.T) {
	text := stackWithEnv + "x-casaos-env: |\n  DB_PASSWORD=secret\n  DB_PORT=13306\n"
	compose, env, err := SplitInstallEnv([]byte(text))
	assert.NilError(t, err)
	assert.Equal(t, string(env), "DB_PASSWORD=secret\nDB_PORT=13306\n")
	assert.Assert(t, !strings.Contains(string(compose), "x-casaos-env"))
	assert.Assert(t, !strings.Contains(string(compose), "secret"))
	// the anchors survive the cut
	assert.Assert(t, strings.Contains(string(compose), "&php"), string(compose))

	// no .env: the text as it came, byte for byte
	compose, env, err = SplitInstallEnv([]byte(stackWithEnv))
	assert.NilError(t, err)
	assert.Assert(t, env == nil)
	assert.Equal(t, string(compose), stackWithEnv)

	_, _, err = SplitInstallEnv([]byte(stackWithEnv + "x-casaos-env:\n  DB_PASSWORD: secret\n"))
	assert.ErrorContains(t, err, "x-casaos-env must be the text of a .env file")
}

func TestAComposeInstalledWithItsEnvIsCheckedResolvedAndKeptAsWritten(t *testing.T) {
	env := []byte("DB_PASSWORD=secret\nDB_PORT=13306\n")
	resolved, kept, err := NewComposeAppWithEnv([]byte(stackWithEnv), env)
	assert.NilError(t, err)

	// resolved: the .env's values, the env_file read, the defaults applied
	db := resolved.Services["db"]
	assert.Equal(t, *db.Environment["MARIADB_PASSWORD"], "secret")
	assert.Equal(t, db.Ports[0].Published, "13306")
	assert.Equal(t, *resolved.Services["app"].Environment["DB_PASSWORD"], "secret") // from env_file: .env
	assert.Equal(t, *resolved.Services["queue"].Deploy.Replicas, 4)
	assert.Equal(t, resolved.Services["app"].Image, "vecto-php:latest")

	// kept: what is written beside the .env still names its keys, and has no secret in it
	text, err := GenerateYAMLFromComposeApp(*kept)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(text), "${DB_PASSWORD:?DB_PASSWORD is missing}"), string(text))
	assert.Assert(t, strings.Contains(string(text), "${DB_PORT}"), string(text))
	assert.Assert(t, !strings.Contains(string(text), "secret"), string(text))
	// a variable the .env does not define takes its default
	assert.Equal(t, *kept.Services["queue"].Deploy.Replicas, 4)
	assert.Equal(t, kept.Name, "vecto")
}

func TestAnEnvThatMissesAKeyOrSetsOneOfCasaOSIsRefused(t *testing.T) {
	_, _, err := NewComposeAppWithEnv([]byte(stackWithEnv), []byte("DB_PORT=13306\n"))
	assert.ErrorContains(t, err, "DB_PASSWORD is missing")

	_, _, err = NewComposeAppWithEnv([]byte(stackWithEnv), []byte("DB_PASSWORD=x\nDB_PORT=1\nTZ=Europe/Paris\n"))
	assert.ErrorContains(t, err, "TZ is set by CasaOS")
}
