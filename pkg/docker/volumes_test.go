package docker_test

import (
	"fmt"
	"testing"

	"github.com/inkly/CasaOS-AppManagement/pkg/docker"
)

func TestGetDir(t *testing.T) {
	fmt.Println(docker.GetDir("", "config"))
}
