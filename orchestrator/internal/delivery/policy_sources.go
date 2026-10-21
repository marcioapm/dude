package delivery

import (
	"encoding/json"
	"reflect"
)

// PolicyKeys are the delivery policy's keys as stored, in Policy's order.
func PolicyKeys() []string {
	t := reflect.TypeFor[Policy]()
	out := make([]string, t.NumField())
	for i := range out {
		out[i] = t.Field(i).Tag.Get("json")
	}
	return out
}

// PolicySources is the policy org and project resolve to (ResolvePolicy)
// and, for each of its keys, the layer the value is taken from: the last
// layer that names it (SetInProject, SetInOrganization), else
// SetInDefault. project is nil for an organisation's own policy.
func PolicySources(org, project json.RawMessage) (Policy, map[string]string, error) {
	p, err := ResolvePolicy(org, project)
	if err != nil {
		return p, nil, err
	}
	var named [2]map[string]json.RawMessage
	_ = json.Unmarshal(org, &named[0])
	_ = json.Unmarshal(project, &named[1])
	out := map[string]string{}
	for _, k := range PolicyKeys() {
		_, inOrg := named[0][k]
		_, inProject := named[1][k]
		switch {
		case inProject:
			out[k] = SetInProject
		case inOrg:
			out[k] = SetInOrganization
		default:
			out[k] = SetInDefault
		}
	}
	return p, out, nil
}
