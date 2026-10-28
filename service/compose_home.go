package service

import (
	"os"
	"os/user"
)

// userHome is $HOME, or the home of the user this process runs as when there is none.
func userHome() (string, error) {
	if dir, err := os.UserHomeDir(); err == nil {
		return dir, nil
	}
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.HomeDir, nil
}
