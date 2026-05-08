package haproxy

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	haproxy "github.com/bcicen/go-haproxy"
	utilwait "k8s.io/apimachinery/pkg/util/wait"
)

const (
	// Prefix for the socket file used for haproxy dynamic API commands.
	afUnixSocketPrefix = "unix://"

	// Prefix if TCP is used to communicate with haproxy.
	tcpSocketPrefix = "tcp://"

	// maxRetries is the number of times a command is retried.
	maxRetries = 3
)

type HAProxyClient interface {
	Execute(cmd string) ([]byte, error)
}

// Client is a client used to dynamically configure haproxy.
type Client struct {
	socketAddress string
	timeout       int
}

// NewClient returns a client used to dynamically change the haproxy config.
func NewClient(socketName string, timeout int) *Client {
	sockAddr := socketName
	if !strings.HasPrefix(sockAddr, afUnixSocketPrefix) && !strings.HasPrefix(sockAddr, tcpSocketPrefix) {
		sockAddr = fmt.Sprintf("%s%s", afUnixSocketPrefix, sockAddr)
	}

	return &Client{
		socketAddress: sockAddr,
		timeout:       timeout,
	}
}

// Execute runs a haproxy dynamic config API command.
func (c *Client) Execute(cmd string) ([]byte, error) {
	log.V(4).Info("running haproxy command", "command", cmd)
	buffer, err := c.runCommandWithRetries(cmd, maxRetries)
	if err != nil {
		log.V(0).Info("haproxy dynamic config API command failed", "command", cmd, "error", err)
		return nil, err
	}

	response := buffer.Bytes()
	log.V(4).Info("haproxy command returned", "response", string(response))
	return response, nil
}

// runCommandWithRetries retries a haproxy command upto the retry limit
// if the error for the command is a retryable error.
func (c *Client) runCommandWithRetries(cmd string, limit int) (*bytes.Buffer, error) {
	var buffer *bytes.Buffer
	var cmdErr error

	cmdWaitBackoff := utilwait.Backoff{
		Duration: 10 * time.Millisecond,
		Factor:   2,
		Steps:    limit,
	}

	n := 0
	utilwait.ExponentialBackoff(cmdWaitBackoff, func() (bool, error) {
		n++
		client := &haproxy.HAProxyClient{
			Addr:    c.socketAddress,
			Timeout: c.timeout,
		}
		buffer, cmdErr = client.RunCommand(cmd)
		if cmdErr == nil {
			return true, nil
		}
		if !isRetriable(cmdErr) {
			return false, cmdErr
		}
		return false, nil
	})

	if cmdErr != nil {
		log.V(4).Info("failed attempt to run haproxy command", "command", cmd, "attempts", n, "error", cmdErr)
	}

	return buffer, cmdErr
}

// isRetriable checks if a haproxy command can be retried.
func isRetriable(err error) bool {
	retryableErrors := []string{
		"connection reset by peer",
		"connection refused",
	}

	s := err.Error()
	for _, v := range retryableErrors {
		if strings.HasSuffix(s, v) {
			return true
		}
	}

	return false
}
