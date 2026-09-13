package service

import (
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
)

// The catalogue entries this exists for, as they are written.
const psiTransfer = `name: psitransfer
services:
  psitransfer:
    image: psitrax/psitransfer:v2.1.0
    ports:
      - target: "3000"
        published: "13001"
        protocol: tcp
    healthcheck:
      retries: "3"
x-casaos:
  main: psitransfer
`

func TestAQuotedNumberIsANumberWhereTheSpecificationWantsOne(t *testing.T) {
	// the loader logs; alone in a run, nothing else has set the logger up
	logger.LogInitConsoleOnly()

	app, err := NewComposeAppFromYAML([]byte(psiTransfer), true, false)
	if err != nil {
		t.Fatalf("the file must load: %v", err)
	}

	service := app.Services["psitransfer"]
	if len(service.Ports) != 1 || service.Ports[0].Target != 3000 || service.Ports[0].Published != "13001" {
		t.Fatalf("ports: %+v", service.Ports)
	}
	if service.HealthCheck == nil || service.HealthCheck.Retries == nil || *service.HealthCheck.Retries != 3 {
		t.Fatalf("healthcheck: %+v", service.HealthCheck)
	}
}

// A published port written as a range is a string the specification allows,
// and a string a person meant as a string is not ours to change.
func TestOnlyDigitOnlyStringsInIntegerFieldsAreTouched(t *testing.T) {
	in := []byte(`name: demo
services:
  web:
    image: nginx
    ports:
      - target: 80
        published: "8000-8010"
    environment:
      PORT: "3000"
      RETRIES: "3"
`)
	out := coerceNumericStrings(in)
	if string(out) != string(in) {
		t.Fatalf("nothing to coerce, so the bytes must be the input itself:\n%s", out)
	}
}

func TestAFileWithNothingToCoerceIsPassedThroughUntouched(t *testing.T) {
	in := []byte("# a comment that a marshaller would drop\nname: demo\nservices:\n  web:\n    image: nginx\n")
	out := coerceNumericStrings(in)
	if &out[0] != &in[0] {
		t.Fatal("the same slice, not a copy")
	}
}

func TestCoercedOutputIsStillTheSameCompose(t *testing.T) {
	out := string(coerceNumericStrings([]byte(psiTransfer)))
	for _, want := range []string{"target: 3000", "published: 13001", "retries: 3", "image: psitrax/psitransfer:v2.1.0", "main: psitransfer"} {
		if !strings.Contains(out, want) {
			t.Fatalf("want %q in:\n%s", want, out)
		}
	}
}
