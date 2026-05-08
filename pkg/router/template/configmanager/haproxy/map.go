package haproxy

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	templateutil "github.com/openshift/router/pkg/router/template/util"
)

// HAProxyMap is a structure representing an haproxy map.
type HAProxyMap struct {
	// name is the haproxy specific name for this map.
	name string

	// client is the haproxy dynamic API client.
	client HAProxyClient
}

// newMapClient returns a new HAProxyMap representing a haproxy map.
func newMapClient(client HAProxyClient, workingDir, name string) *HAProxyMap {
	return &HAProxyMap{
		name:   path.Join(workingDir, "conf", name),
		client: client,
	}
}

// SyncEntries applies the provided map entries into an HAProxy map. The new content is applied
// atomically, and in the correct order to avoid wrong match in case of path overlap.
func (m *HAProxyMap) SyncEntries(entries configEntryMap) error {
	// Produces HAProxy's compatible map entry lines based on the newEntries hashmap:
	// key is the host+path match; value is the backend name.
	var lines []string
	for k, v := range entries {
		lines = append(lines, k+" "+string(v))
	}

	// Sort entries to avoid wrong match, see https://issues.redhat.com/browse/OCPBUGS-75009
	// Also, it produces a predictable order, since the source of data is a hashmap.
	lines = templateutil.SortMapPaths(lines, `^[^\.]*\.`)

	// atomically replacing a map is a three steps workflow:
	// - prepare map <name>: creates a new and empty version
	// - add map @<version> <name> <<: receives a payload with new content
	// - commit map @<version>: atomically replaces the new content

	// preparing and acquiring the transaction version
	prepareResponseRaw, err := m.client.Execute("prepare map " + m.name)
	if err != nil {
		return err
	}
	prepareResponse := strings.TrimSpace(string(prepareResponseRaw))
	versionStr := strings.TrimPrefix(prepareResponse, "New version created: ")
	if prepareResponse == versionStr {
		return fmt.Errorf("unrecognized response preparing a new map: %s", prepareResponse)
	}
	version, _ := strconv.Atoi(versionStr)
	if version <= 0 {
		return fmt.Errorf("invalid map version: %s", versionStr)
	}

	// adding the new payload if any, otherwise skip to `commit map` which removes the content
	if len(lines) > 0 {
		cmdAddMap := &strings.Builder{}
		_, _ = fmt.Fprintf(cmdAddMap, "add map @%d %s <<\n", version, m.name)
		for _, line := range lines {
			_, _ = fmt.Fprintln(cmdAddMap, line)
		}
		addMapResponseRaw, err := m.client.Execute(cmdAddMap.String())
		if err != nil {
			return err
		}
		addMapResponse := strings.TrimSpace(string(addMapResponseRaw))
		if addMapResponse != "" {
			return fmt.Errorf("unrecognized response adding new map content: %s", addMapResponse)
		}
	}

	// commiting the new content, or removing the old content in case `add map` was skipped
	commitMapResponseRaw, err := m.client.Execute(fmt.Sprintf("commit map @%d %s", version, m.name))
	if err != nil {
		return err
	}
	commitMapResponse := strings.TrimSpace(string(commitMapResponseRaw))
	if commitMapResponse != "" {
		return fmt.Errorf("unrecognized response commiting new map content: %s", commitMapResponse)
	}

	return nil
}
