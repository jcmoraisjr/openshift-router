package haproxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHAProxyMapSyncEntries tests adding/replacing/removing entries in a haproxy map.
func TestHAProxyMapSyncEntries(t *testing.T) {
	const workingDir = "/var/haproxy/lib"
	const mapName = "backends.map"

	testCases := map[string]struct {
		entries       configEntryMap
		cmdCustomResp []string // 1:1 to `cmdExpected`, trailing empty items can be omited.
		errExpected   string
		cmdExpected   []string
	}{
		"should remove all entries on empty map": {
			entries: configEntryMap{},
			cmdCustomResp: []string{
				"New version created: 1",
			},
			cmdExpected: []string{
				"prepare map /var/haproxy/lib/conf/backends.map",
				"commit map @1 /var/haproxy/lib/conf/backends.map",
			},
		},
		"should apply entry having one item": {
			entries: configEntryMap{
				"host1.local/path": "back1",
			},
			cmdCustomResp: []string{
				"New version created: 2",
			},
			cmdExpected: []string{
				"prepare map /var/haproxy/lib/conf/backends.map",
				`add map @2 /var/haproxy/lib/conf/backends.map <<
host1.local/path back1
`,
				"commit map @2 /var/haproxy/lib/conf/backends.map",
			},
		},
		"should apply more than one item in the correct order": {
			entries: configEntryMap{
				"host1.local/path1": "back1:1",
				"host1.local/path2": "back1:2",
				"host1.local/path3": "back1:3",
			},
			cmdCustomResp: []string{
				"New version created: 3",
			},
			cmdExpected: []string{
				"prepare map /var/haproxy/lib/conf/backends.map",
				`add map @3 /var/haproxy/lib/conf/backends.map <<
host1.local/path3 back1:3
host1.local/path2 back1:2
host1.local/path1 back1:1
`,
				"commit map @3 /var/haproxy/lib/conf/backends.map",
			},
		},
		"should fail on invalid version number": {
			entries: configEntryMap{},
			cmdCustomResp: []string{
				"New version created: not-number",
			},
			errExpected: `invalid map version: not-number`,
			cmdExpected: []string{
				"prepare map /var/haproxy/lib/conf/backends.map",
			},
		},
		"should fail on invalid prepare map response": {
			entries: configEntryMap{},
			cmdCustomResp: []string{
				"Some unknown prepare map error",
			},
			errExpected: `unrecognized response preparing a new map: Some unknown prepare map error`,
			cmdExpected: []string{
				"prepare map /var/haproxy/lib/conf/backends.map",
			},
		},
		"should fail on invalid add map response": {
			entries: configEntryMap{
				"host1.local/path": "back1",
			},
			cmdCustomResp: []string{
				"New version created: 1",
				"Some unknown add map error",
			},
			errExpected: `unrecognized response adding new map content: Some unknown add map error`,
			cmdExpected: []string{
				"prepare map /var/haproxy/lib/conf/backends.map",
				`add map @1 /var/haproxy/lib/conf/backends.map <<
host1.local/path back1
`,
			},
		},
		"should fail on invalid commit map response": {
			entries: configEntryMap{
				"host1.local/path": "back1",
			},
			cmdCustomResp: []string{
				"New version created: 1",
				"",
				"Some unknown commit map error",
			},
			errExpected: `unrecognized response commiting new map content: Some unknown commit map error`,
			cmdExpected: []string{
				"prepare map /var/haproxy/lib/conf/backends.map",
				`add map @1 /var/haproxy/lib/conf/backends.map <<
host1.local/path back1
`,
				"commit map @1 /var/haproxy/lib/conf/backends.map",
			},
		},
	}

	for name, test := range testCases {
		t.Run(name, func(t *testing.T) {
			client := &fakeClient{cmdCustomResp: test.cmdCustomResp}
			m := newMapClient(client, workingDir, mapName)
			err := m.SyncEntries(test.entries)
			if test.errExpected != "" {
				require.EqualError(t, err, test.errExpected)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, client.executedCmds, test.cmdExpected)
		})
	}
}
