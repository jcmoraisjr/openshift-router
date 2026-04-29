package haproxy

import (
	"strings"
	"testing"

	routev1 "github.com/openshift/api/route/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	templaterouter "github.com/openshift/router/pkg/router/template"
)

func TestReplaceRouteEndpoints(t *testing.T) {
	ep1 := templaterouter.Endpoint{ID: "ep1", IP: "10.0.1.11", Port: "8080", Weight: 1}
	ep2 := templaterouter.Endpoint{ID: "ep2", IP: "10.0.1.12", Port: "8080", Weight: 1}
	ep3 := templaterouter.Endpoint{ID: "ep3", IP: "10.0.1.13", Port: "8080", Weight: 1}

	ep2weight2 := ep2
	ep2weight2.Weight = 2

	ep2h2 := ep2
	ep2h2.AppProtocol = "h2c"

	testCases := map[string]struct {
		oldEndpoints    []templaterouter.Endpoint
		newEndpoints    []templaterouter.Endpoint
		activeEndpoints int
		cmdCustomResp   []string
		expectedCmds    []string
		expectedError   string
	}{
		"adding one endpoint": {
			newEndpoints: []templaterouter.Endpoint{ep1},
			expectedCmds: []string{
				"add server be_http:service1/ep1 10.0.1.11:8080 weight 1 check inter 5000ms",
				"set server be_http:service1/ep1 state ready",
				"disable health be_http:service1/ep1",
				"set server be_http:service1/ep1 health up",
			},
		},
		"adding one endpoint having more than one service": {
			newEndpoints: []templaterouter.Endpoint{ep1},
			// two activeEndpoints: one endpoint from this service, another one from the second service,
			// so health check should be enabled.
			activeEndpoints: 2,
			expectedCmds: []string{
				"add server be_http:service1/ep1 10.0.1.11:8080 weight 1 check inter 5000ms",
				"set server be_http:service1/ep1 state ready",
				"enable health be_http:service1/ep1",
			},
		},
		"adding two endpoints": {
			newEndpoints: []templaterouter.Endpoint{ep1, ep2},
			expectedCmds: []string{
				"add server be_http:service1/ep1 10.0.1.11:8080 weight 1 check inter 5000ms",
				"set server be_http:service1/ep1 state ready",
				"add server be_http:service1/ep2 10.0.1.12:8080 weight 1 check inter 5000ms",
				"set server be_http:service1/ep2 state ready",
				"enable health be_http:service1/ep1",
				"enable health be_http:service1/ep2",
			},
		},
		"scaling out to two": {
			oldEndpoints: []templaterouter.Endpoint{ep1},
			newEndpoints: []templaterouter.Endpoint{ep1, ep2},
			expectedCmds: []string{
				"add server be_http:service1/ep2 10.0.1.12:8080 weight 1 check inter 5000ms",
				"set server be_http:service1/ep2 state ready",
				"enable health be_http:service1/ep1",
				"enable health be_http:service1/ep2",
			},
		},
		"scaling out to two and first server failing to enable health": {
			oldEndpoints: []templaterouter.Endpoint{ep1},
			newEndpoints: []templaterouter.Endpoint{ep1, ep2},
			cmdCustomResp: []string{
				"",
				"",
				"Some unknown enable health check error",
			},
			expectedError: `unexpected response from haproxy: Some unknown enable health check error`,
			expectedCmds: []string{
				"add server be_http:service1/ep2 10.0.1.12:8080 weight 1 check inter 5000ms",
				"set server be_http:service1/ep2 state ready",
				"enable health be_http:service1/ep1",
				"enable health be_http:service1/ep2",
			},
		},
		"scaling out to two and first server not having health check configured": {
			oldEndpoints: []templaterouter.Endpoint{ep1},
			newEndpoints: []templaterouter.Endpoint{ep1, ep2},
			cmdCustomResp: []string{
				"",
				"",
				"Health check was not configured on this server",
			},
			expectedCmds: []string{
				"add server be_http:service1/ep2 10.0.1.12:8080 weight 1 check inter 5000ms",
				"set server be_http:service1/ep2 state ready",
				"enable health be_http:service1/ep1",
				"set server be_http:service1/ep1 state maint",
				"del server be_http:service1/ep1",
				"add server be_http:service1/ep1 10.0.1.11:8080 weight 1 check inter 5000ms",
				"set server be_http:service1/ep1 state ready",
				"enable health be_http:service1/ep2",
				"enable health be_http:service1/ep1",
			},
		},
		"scaling out to three": {
			oldEndpoints: []templaterouter.Endpoint{ep1, ep2},
			newEndpoints: []templaterouter.Endpoint{ep1, ep2, ep3},
			expectedCmds: []string{
				"add server be_http:service1/ep3 10.0.1.13:8080 weight 1 check inter 5000ms",
				"set server be_http:service1/ep3 state ready",
				"enable health be_http:service1/ep3",
			},
		},
		"scaling in to two": {
			oldEndpoints: []templaterouter.Endpoint{ep1, ep2, ep3},
			newEndpoints: []templaterouter.Endpoint{ep1, ep2},
			expectedCmds: []string{
				"set server be_http:service1/ep3 state maint",
				"del server be_http:service1/ep3",
			},
		},
		"scaling in to one": {
			oldEndpoints: []templaterouter.Endpoint{ep1, ep2},
			newEndpoints: []templaterouter.Endpoint{ep1},
			expectedCmds: []string{
				"set server be_http:service1/ep2 state maint",
				"del server be_http:service1/ep2",
				"disable health be_http:service1/ep1",
				"set server be_http:service1/ep1 health up",
			},
		},
		"scaling in to one having more than one service": {
			oldEndpoints: []templaterouter.Endpoint{ep1, ep2},
			newEndpoints: []templaterouter.Endpoint{ep1},
			// two activeEndpoints: one endpoint from this service, another one from the second service,
			// so health check shouldn't be disabled.
			activeEndpoints: 2,
			expectedCmds: []string{
				"set server be_http:service1/ep2 state maint",
				"del server be_http:service1/ep2",
			},
		},
		"changing weight": {
			oldEndpoints: []templaterouter.Endpoint{ep1, ep2},
			newEndpoints: []templaterouter.Endpoint{ep1, ep2weight2},
			expectedCmds: []string{
				"set server be_http:service1/ep2 weight 2",
			},
		},
		"changing backend protocol": {
			oldEndpoints: []templaterouter.Endpoint{ep1, ep2},
			newEndpoints: []templaterouter.Endpoint{ep1, ep2h2},
			expectedCmds: []string{
				"set server be_http:service1/ep2 state maint",
				"del server be_http:service1/ep2",
				"add server be_http:service1/ep2 10.0.1.12:8080 weight 1 proto h2 check inter 5000ms",
				"set server be_http:service1/ep2 state ready",
				"enable health be_http:service1/ep2",
			},
		},
	}

	for name, test := range testCases {
		t.Run(name, func(t *testing.T) {
			if test.activeEndpoints == 0 {
				test.activeEndpoints = len(test.newEndpoints)
			}
			require.GreaterOrEqual(t, test.activeEndpoints, len(test.newEndpoints), "activeEndpoints should be 0 (zero) or >=len(newEndpoints)")

			opt := templaterouter.ConfigManagerOptions{}
			svckey := templaterouter.ServiceAliasConfigKey("service1")
			var svc templaterouter.ServiceUnit
			backend := templaterouter.ServiceAliasConfig{}
			route := routev1.Route{}

			client := fakeClient{
				cmdCustomResp: test.cmdCustomResp,
			}

			cm := newHAProxyConfigManager(opt, &client)
			cm.Register(svckey, &backend, &route)
			err := cm.ReplaceRouteEndpoints(svckey, &svc, test.oldEndpoints, test.newEndpoints, test.activeEndpoints)
			if test.expectedError == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, strings.TrimSpace(test.expectedError))
			}
			assert.Equal(t, test.expectedCmds, client.executedCmds)
		})
	}
}

func TestRemoveRouteEndpoints(t *testing.T) {
	testCases := map[string]struct {
		endpoints     []templaterouter.Endpoint
		cmdCustomResp []string
		expectedCmds  []string
		expectedError string
	}{
		"single endpoint": {
			endpoints: []templaterouter.Endpoint{{ID: "ep1"}},
			expectedCmds: []string{
				"set server be_http:service1/ep1 state maint",
				"del server be_http:service1/ep1",
			},
		},
		"two endpoints failing first": {
			endpoints: []templaterouter.Endpoint{{ID: "ep1"}, {ID: "ep2"}},
			cmdCustomResp: []string{
				"some error setting maintenance",
			},
			expectedCmds: []string{
				"set server be_http:service1/ep1 state maint",
				"set server be_http:service1/ep2 state maint",
				"del server be_http:service1/ep2",
			},
			expectedError: `error deleting server ep1: unexpected response from haproxy: some error setting maintenance`,
		},
		"two endpoints failing both": {
			endpoints: []templaterouter.Endpoint{{ID: "ep1"}, {ID: "ep2"}},
			cmdCustomResp: []string{
				"some error setting maintenance on endpoint1",
				"some error setting maintenance on endpoint2",
			},
			expectedCmds: []string{
				"set server be_http:service1/ep1 state maint",
				"set server be_http:service1/ep2 state maint",
			},
			expectedError: `
error deleting server ep1: unexpected response from haproxy: some error setting maintenance on endpoint1
error deleting server ep2: unexpected response from haproxy: some error setting maintenance on endpoint2
`,
		},
		"two endpoints failing first to delete": {
			endpoints: []templaterouter.Endpoint{{ID: "ep1"}, {ID: "ep2"}},
			cmdCustomResp: []string{
				"",                           // first cmd succeed
				"some error deleting server", // failing second cmd, first delete
			},
			expectedCmds: []string{
				"set server be_http:service1/ep1 state maint",
				"del server be_http:service1/ep1",
				"set server be_http:service1/ep2 state maint",
				"del server be_http:service1/ep2",
			},
			expectedError: "", // should have no error
		},
	}

	for name, test := range testCases {
		t.Run(name, func(t *testing.T) {
			opt := templaterouter.ConfigManagerOptions{}
			svckey := templaterouter.ServiceAliasConfigKey("service1")
			backend := templaterouter.ServiceAliasConfig{}
			route := routev1.Route{}

			client := fakeClient{
				cmdCustomResp: test.cmdCustomResp,
			}

			cm := newHAProxyConfigManager(opt, &client)
			cm.Register(svckey, &backend, &route)
			err := cm.RemoveRouteEndpoints(svckey, test.endpoints)
			if test.expectedError == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, strings.TrimSpace(test.expectedError))
			}
			require.Equal(t, test.expectedCmds, client.executedCmds)
		})
	}
}

func newHAProxyConfigManager(opt templaterouter.ConfigManagerOptions, client HAProxyClient) *haproxyConfigManager {
	router := templaterouter.NewFakeTemplateRouter()
	cm := NewHAProxyConfigManager(opt)
	cm.Initialize(router, "")
	cm.client = client
	return cm
}
