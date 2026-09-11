// Package rclone talks to the rclone daemon this distribution already installs.
//
// The installer puts rclone on every box and runs it as a service:
//
//	ExecStart=/usr/bin/rclone rcd --rc-addr unix:///var/run/rclone/rclone.sock --rc-no-auth
//
// which means S3, SFTP, FTP, the retries, the resume after a broken connection and
// the incremental comparison are already here, already running, and already driven
// over an API. None of that is worth writing again in Go.
//
// Two things about that daemon are worth knowing before trusting it with
// credentials. It listens with --rc-no-auth, so any local process can read and
// write every remote it holds; that is already true of the cloud drives CasaOS
// stores there today. And it is pinned at v1.61.1, which is old but long predates
// every backend used here.
package rclone

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

// SocketPath is where the unit file puts the daemon's socket.
const SocketPath = "/var/run/rclone/rclone.sock"

// DestinationPrefix marks a remote as one of ours.
//
// Backup destinations live in rclone's own config, next to the cloud drives
// CasaOS mounts -- which is deliberate: the credentials then sit where this
// project already keeps credentials of exactly that kind, rather than in a second
// store invented for the purpose. The prefix is what tells the two apart, and it
// is readable in `rclone config` by anyone wondering what CasaOS put there.
const DestinationPrefix = "casaos-backup-"

// ErrDaemonUnreachable is returned when the socket is not there or not answering.
// It is worth its own error: it means rclone is not running, which is a different
// problem from a destination being misconfigured, and the two get different advice.
var ErrDaemonUnreachable = errors.New("the rclone daemon is not answering on " + SocketPath)

// Client drives the rclone daemon over its unix socket.
type Client struct {
	http *resty.Client
}

// NewClient returns a client bound to the daemon's socket.
//
// The timeout here covers control calls only -- listing remotes, creating one,
// checking one answers. A transfer is started as a job and polled, so no copy ever
// has to finish inside one request.
func NewClient() *Client {
	transport := &http.Transport{
		Dial: func(_, _ string) (net.Conn, error) {
			return net.Dial("unix", SocketPath)
		},
	}

	http := resty.New().
		SetTransport(transport).
		SetBaseURL("http://localhost").
		SetTimeout(30 * time.Second)

	return &Client{http: http}
}

// call posts to one of the daemon's rc endpoints.
//
// Parameters go as form data rather than JSON because that is what the daemon
// accepts for these, and because it keeps a credential out of a JSON body that
// would otherwise be easy to log whole.
func (c *Client) call(endpoint string, params map[string]string, out interface{}) error {
	response, err := c.http.R().SetFormData(params).Post(endpoint)
	if err != nil {
		// A socket that is not there is the common case, and it reads as a
		// connection error rather than anything rclone said.
		return fmt.Errorf("%w: %s", ErrDaemonUnreachable, err)
	}

	if response.StatusCode() != http.StatusOK {
		// The daemon answers errors as {"error": "..."} and the message is the
		// useful half -- the status alone says nothing a person can act on.
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(response.Body(), &failure) == nil && failure.Error != "" {
			return fmt.Errorf("rclone: %s", failure.Error)
		}

		return fmt.Errorf("rclone answered %d to %s", response.StatusCode(), endpoint)
	}

	if out == nil {
		return nil
	}

	return json.Unmarshal(response.Body(), out)
}

// Destinations lists the remotes this project put there, by their short name --
// the prefix is an implementation detail and does not belong in an interface.
func (c *Client) Destinations() ([]string, error) {
	var result struct {
		Remotes []string `json:"remotes"`
	}

	if err := c.call("/config/listremotes", nil, &result); err != nil {
		return nil, err
	}

	names := []string{}
	for _, remote := range result.Remotes {
		if short, ok := strings.CutPrefix(remote, DestinationPrefix); ok {
			names = append(names, short)
		}
	}

	return names, nil
}

// CreateDestination adds or replaces a backup destination.
//
// `backend` is an rclone backend name -- "s3", "sftp", "ftp" -- and `parameters`
// are that backend's own options. Neither is interpreted here: rclone validates
// them, rclone knows what each backend needs, and a list of required fields
// maintained on this side would go stale the first time rclone gained an option.
func (c *Client) CreateDestination(name, backend string, parameters map[string]string) error {
	if name == "" {
		return errors.New("a destination needs a name")
	}
	if backend == "" {
		return errors.New("a destination needs a backend, such as s3, sftp or ftp")
	}

	encoded, err := json.Marshal(parameters)
	if err != nil {
		return err
	}

	return c.call("/config/create", map[string]string{
		"name":       DestinationPrefix + name,
		"type":       backend,
		"parameters": string(encoded),
	}, nil)
}

// DeleteDestination forgets a destination and its credentials. Whatever was
// already copied to it stays where it is: this removes the way in, not the backup.
func (c *Client) DeleteDestination(name string) error {
	return c.call("/config/delete", map[string]string{
		"name": DestinationPrefix + name,
	}, nil)
}

// DestinationSpace is what a destination says about itself when asked.
type DestinationSpace struct {
	// Used and Free are bytes, and either may be absent: plenty of backends,
	// object stores especially, have no answer to "how much room is left", and
	// inventing one would be worse than saying nothing.
	Used  *int64 `json:"used,omitempty"`
	Free  *int64 `json:"free,omitempty"`
	Total *int64 `json:"total,omitempty"`
}

// CheckDestination asks a destination whether it is reachable, and what it can say
// about its space.
//
// Worth doing at the moment somebody configures a destination rather than at the
// moment a backup runs: a typo in a bucket name found at 3am by a scheduled job
// nobody is watching is a backup that never existed.
func (c *Client) CheckDestination(name string) (DestinationSpace, error) {
	var space DestinationSpace

	err := c.call("/operations/about", map[string]string{
		"fs": DestinationPrefix + name + ":",
	}, &space)

	return space, err
}
