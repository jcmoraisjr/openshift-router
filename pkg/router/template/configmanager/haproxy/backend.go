package haproxy

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	routev1 "github.com/openshift/api/route/v1"
	templaterouter "github.com/openshift/router/pkg/router/template"
)

func newBackendClient(client HAProxyClient, backendName templaterouter.ServiceAliasConfigKey) *Backend {
	return &Backend{
		client: client,
		name:   backendName,
	}
}

// Backend represents a specific haproxy backend.
type Backend struct {
	client HAProxyClient
	name   templaterouter.ServiceAliasConfigKey
}

// SetRoutingKey sets the cookie routing key for the haproxy backend.
func (b *Backend) SetRoutingKey(k string) error {
	if err := b.innerSetDynamicCookie(k); err != nil {
		return err
	}
	return b.innerEnableDynamicCookie()
}

// IsHealthCheckNotConfiguredError returns true if the provided error is due to an attempt to
// enable health check on a backend server whose health check was not configured.
func (b *Backend) IsHealthCheckNotConfiguredError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Health check was not configured on this server")
}

// IsServerAlreadyExistsError returns true if the provided error is due to an attempt to add a
// new backend server, but the server name was already used.
func (b *Backend) IsServerAlreadyExistsError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Already exists a server ")
}

// AddServer dynamically adds a new backend server. It detects if the server already exists, and if so tries to remove it.
// It returns a failure in case HAProxy refuses to dynamically add the server for any reason, or if the existing server
// cannot be removed, e.g., it still have active or steady and established connection(s) to its backend server endpoint.
func (b *Backend) AddServer(cfg *templaterouter.ServiceAliasConfig, svc *templaterouter.ServiceUnit, ep *templaterouter.Endpoint, workingDir, defaultDestinationCA string) error {
	if err := b.innerAddServer(cfg, svc, ep, workingDir, defaultDestinationCA); err != nil {
		if !b.IsServerAlreadyExistsError(err) {
			return err
		}
		// Failed due to already existing server left behind, in maintenance mode, due to in-flight connections.
		// Let's just give it another chance to be deleted.
		if err := b.innerDeleteServer(ep); err != nil {
			// No way, need to fail which will ask for a fork-and-reload. This will leave the existing connections in the old process.
			return err
		}
		if err := b.innerAddServer(cfg, svc, ep, workingDir, defaultDestinationCA); err != nil {
			return err
		}
	}
	if err := b.innerSetServerState(ep, true); err != nil {
		return err
	}

	// health check is disabled by default on new backend servers, its enablement is handled via cm.ReplaceRouteEndpoints(),
	// since that method has a better view of former and current active backend servers.
	return nil
}

// UpdateServer dynamically updates the backend server with new address and weight.
func (b *Backend) UpdateServer(cfg *templaterouter.ServiceAliasConfig, svc *templaterouter.ServiceUnit, oldEP, newEP *templaterouter.Endpoint, isPassthrough bool, workingDir, defaultDestinationCA string) (added bool, err error) {
	oldIsH2 := strings.TrimPrefix(oldEP.AppProtocol, "kubernetes.io/") == "h2c"
	newIsH2 := strings.TrimPrefix(newEP.AppProtocol, "kubernetes.io/") == "h2c"
	if oldIsH2 != newIsH2 || oldEP.VerifyHostname != newEP.VerifyHostname {
		// changes require to remove+add endpoints, an error is returned in case this cannot be done, e.g., existing connections
		return true, b.ReplaceServer(cfg, svc, oldEP, newEP, workingDir, defaultDestinationCA)
	}

	// changes that can be applied in the running server
	if oldEP.IP != newEP.IP || oldEP.Port != newEP.Port {
		if err := b.innerUpdateServerAddrPort(newEP); err != nil {
			return false, err
		}
	}

	if oldEP.Weight != newEP.Weight {
		return false, b.innerUpdateServerWeight(newEP, isPassthrough)
	}

	return false, nil
}

// ReplaceServer dynamically replaces the backend server by removing it and adding again with new configuration.
func (b *Backend) ReplaceServer(cfg *templaterouter.ServiceAliasConfig, svc *templaterouter.ServiceUnit, oldEP, newEP *templaterouter.Endpoint, workingDir, defaultDestinationCA string) error {
	if err := b.innerSetServerState(oldEP, false); err != nil {
		return err
	}
	if err := b.innerDeleteServer(oldEP); err != nil {
		return err
	}
	if err := b.innerAddServer(cfg, svc, newEP, workingDir, defaultDestinationCA); err != nil {
		return err
	}
	return b.innerSetServerState(newEP, true)
}

// EnableHealthCheck dynamically enables health check on a backend server that already declares the health check interval.
func (b *Backend) EnableHealthCheck(ep *templaterouter.Endpoint) error {
	return b.innerSetHealthCheck(ep, true)
}

// DisableHealthCheck dynamically disables health check on a backend server.
func (b *Backend) DisableHealthCheck(ep *templaterouter.Endpoint) error {
	if err := b.innerSetHealthCheck(ep, false); err != nil {
		return err
	}
	// manually set the new health state after disabling the automatic check
	return b.innerSetServerHealth(ep, true)
}

// DeleteServer dynamically removes the backend server from the load balance. The backend server is put in maintenance mode
// and returns `removed` as false in case it has active or steady and established connections, so these connections continue
// to be handled and new ones are directed to other servers. An error only happens if the server cannot be put in maintenance
// mode, any failure trying to remove the server is logged and just return removed as false.
func (b *Backend) DeleteServer(ep *templaterouter.Endpoint) (removed bool, err error) {
	// put in maintenance mode first, this is a pre-requisite to remove a backend server.
	if err := b.innerSetServerState(ep, false); err != nil {
		return false, err
	}
	if err := b.innerDeleteServer(ep); err != nil {
		log.Info("disabling backend server instead of deleting due to a delete failure", "server", ep.ID, "error", err.Error())
		return false, nil
	}
	return true, nil
}

func (b *Backend) innerAddServer(cfg *templaterouter.ServiceAliasConfig, svc *templaterouter.ServiceUnit, ep *templaterouter.Endpoint, workingDir, defaultDestinationCA string) error {
	// This should always follow the template, changes here should be reflected there, both regular and passthrough backends
	//
	// TODO: either read this configuration from the template, or instead make the template read from here.
	// For the former, note that creating a new template definition should conflict with the for-loop in templateRouter.writeConfig()
	// that assumes that all the definitions should be written to disk.
	//
	// https://redhat.atlassian.net/browse/NE-2646

	cmd := fmt.Sprintf("add server %s/%s %s:%s weight %d", b.name, ep.ID, ep.IP, ep.Port, ep.Weight)

	switch cfg.TLSTermination {
	case routev1.TLSTerminationReencrypt:
		cmd += " ssl"
		if disableHTTP2, _ := strconv.ParseBool(os.Getenv("ROUTER_DISABLE_HTTP2")); !disableHTTP2 {
			cmd += " alpn h2,http/1.1"
		}
		if ep.VerifyHostname {
			cmd += " verifyhost " + svc.Hostname
		}
		if cert := cfg.Certificates[cfg.Host+"_pod"]; len(cert.Contents) > 0 {
			cmd += " verify required ca-file " + path.Join(workingDir, "router/cacerts", cert.ID+".pem")
		} else if len(defaultDestinationCA) > 0 {
			cmd += " verify required ca-file " + defaultDestinationCA
		} else {
			cmd += " verify none"
		}
	case "", routev1.TLSTerminationEdge:
		if strings.TrimPrefix(ep.AppProtocol, "kubernetes.io/") == "h2c" {
			cmd += " proto h2"
		}
	case routev1.TLSTerminationPassthrough:
		// passthrough is a TCP listener and does not use ssl or proto related config
	}

	// health check is always configured and defaults as disabled, being enabled later
	// on DCM's manager depending on `cfg.ActiveEndpoints > 1` and `!ep.NoHealthCheck`.
	inter := templaterouter.FirstMatch(`[1-9][0-9]*(us|ms|s|m|h|d)?`,
		cfg.Annotations["router.openshift.io/haproxy.health.check.interval"],
		os.Getenv("ROUTER_BACKEND_CHECK_INTERVAL"),
		"5000ms")
	cmd += " check inter " + inter

	podMaxConn := cfg.Annotations["haproxy.router.openshift.io/pod-concurrent-connections"]
	if _, err := strconv.Atoi(podMaxConn); err == nil {
		cmd += " maxconn " + podMaxConn
	}

	return execCommand(b.client, apiAddServer, cmd)
}

func (b *Backend) innerUpdateServerAddrPort(ep *templaterouter.Endpoint) error {
	cmd := fmt.Sprintf("set server %s/%s addr %s port %s", b.name, ep.ID, ep.IP, ep.Port)
	return execCommand(b.client, apiSetServerAddr, cmd)
}

func (b *Backend) innerUpdateServerWeight(ep *templaterouter.Endpoint, isPassthrough bool) error {
	// https://github.com/openshift/router/blob/896390778ebe15f57f87e6ca78f11c96e64c2652/pkg/router/template/configmanager/haproxy/manager.go#L446-L454
	weight := "100%" // hardcoded for passthrough
	if !isPassthrough {
		weight = strconv.Itoa(int(ep.Weight))
	}
	cmd := fmt.Sprintf("set server %s/%s weight %s", b.name, ep.ID, weight)
	return execCommand(b.client, apiSetServerWeight, cmd)
}

func (b *Backend) innerSetHealthCheck(ep *templaterouter.Endpoint, enable bool) error {
	enableStr := "enable"
	if !enable {
		enableStr = "disable"
	}
	cmd := fmt.Sprintf("%s health %s/%s", enableStr, b.name, ep.ID)
	return execCommand(b.client, apiSetHealth, cmd)
}

func (b *Backend) innerSetServerHealth(ep *templaterouter.Endpoint, up bool) error {
	upStr := "up"
	if !up {
		upStr = "down"
	}
	cmd := fmt.Sprintf("set server %s/%s health %s", b.name, ep.ID, upStr)
	return execCommand(b.client, apiSetServerHealth, cmd)
}

func (b *Backend) innerSetServerState(ep *templaterouter.Endpoint, ready bool) error {
	state := "ready"
	if !ready {
		state = "maint"
	} else if ep.Weight <= 0 {
		state = "drain"
	}
	cmd := fmt.Sprintf("set server %s/%s state %s", b.name, ep.ID, state)
	return execCommand(b.client, apiSetServerState, cmd)
}

func (b *Backend) innerSetDynamicCookie(key string) error {
	cmd := fmt.Sprintf("set dynamic-cookie-key backend %s %s", b.name, key)
	return execCommand(b.client, apiDynamicCookie, cmd)
}

func (b *Backend) innerEnableDynamicCookie() error {
	cmd := fmt.Sprintf("enable dynamic-cookie backend %s", b.name)
	return execCommand(b.client, apiDynamicCookie, cmd)
}

func (b *Backend) innerDeleteServer(ep *templaterouter.Endpoint) error {
	cmd := fmt.Sprintf("del server %s/%s", b.name, ep.ID)
	return execCommand(b.client, apiDelServer, cmd)
}

type apiType int

const (
	apiAddServer apiType = iota
	apiDelServer
	apiDynamicCookie
	apiSetHealth
	apiSetServerAddr
	apiSetServerHealth
	apiSetServerWeight
	apiSetServerState
)

func execCommand(client HAProxyClient, api apiType, cmd string) error {
	responseRaw, err := client.Execute(cmd)
	if err != nil {
		return err
	}
	response := strings.TrimSpace(string(responseRaw))
	if len(response) == 0 {
		return nil
	}

	var valid bool
	switch api {
	case apiAddServer:
		valid = response == "New server registered."
	case apiDelServer:
		valid = response == "Server deleted."
	case apiSetServerAddr:
		valid = response == "nothing changed" || strings.HasPrefix(response, "IP changed from ") || strings.HasPrefix(response, "port changed from ") || strings.HasPrefix(response, "no need to change ")
	case apiDynamicCookie, apiSetHealth, apiSetServerHealth, apiSetServerWeight, apiSetServerState:
		valid = false // any response from these api calls mean there is a failure
	default:
		// fail fast in case of a dev error
		panic(fmt.Errorf("invalid cmd ID: %d", api))
	}

	if !valid {
		return fmt.Errorf("unexpected response from haproxy: %s", response)
	}
	return nil
}
