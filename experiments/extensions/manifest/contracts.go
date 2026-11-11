package manifest

import "encoding/json"

type Operation struct {
	Result       string   `json:"result"`
	Effect       string   `json:"effect"`
	Capabilities []string `json:"capabilities"`
	Events       []string `json:"events"`
}

type Contracts struct {
	Commands map[string]Operation `json:"commands"`
	Queries  map[string]Operation `json:"queries"`
	Events   map[string]string    `json:"events"`
}

func contractsFields(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 {
		return false
	}
	for _, kind := range []string{"commands", "queries"} {
		var ops map[string]map[string]json.RawMessage
		if json.Unmarshal(fields[kind], &ops) != nil || ops == nil {
			return false
		}
		for _, op := range ops {
			if len(op) != 4 {
				return false
			}
			for _, name := range []string{"result", "effect", "capabilities", "events"} {
				if op[name] == nil {
					return false
				}
			}
		}
	}
	return fields["events"] != nil
}

func (m Manifest) validContracts() bool {
	if m.ManifestVersion == 1 {
		return m.Contracts == nil
	}
	c := m.Contracts
	if c == nil || !operations(c.Events) {
		return false
	}
	permissions := map[string]bool{}
	for _, p := range m.Permissions {
		permissions[p] = true
	}
	if !contractOperations(m.Commands, c.Commands, c.Events, permissions, false) {
		return false
	}
	return contractOperations(m.Queries, c.Queries, c.Events, permissions, true)
}

func contractOperations(inputs map[string]string, ops map[string]Operation, events map[string]string, permissions map[string]bool, query bool) bool {
	if ops == nil || len(inputs) != len(ops) {
		return false
	}
	for name, op := range ops {
		if inputs[name] == "" || !schemaPath.MatchString(op.Result) || !validOperation(op, events, permissions, query) {
			return false
		}
	}
	return true
}

func validOperation(op Operation, events map[string]string, permissions map[string]bool, query bool) bool {
	if !unique(op.Capabilities, 2, permissions) || op.Events == nil || len(op.Events) > 16 {
		return false
	}
	caps := map[string]bool{}
	for _, p := range op.Capabilities {
		caps[p] = true
	}
	if op.Effect == "none" {
		return len(op.Events) == 0 && !caps["storage.own"] && !caps["events.own"]
	}
	if query || op.Effect != "mutation" || !caps["storage.own"] {
		return false
	}
	if (len(op.Events) > 0) != caps["events.own"] {
		return false
	}
	seen := map[string]bool{}
	for _, name := range op.Events {
		if events[name] == "" || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

// SchemaPaths перечисляет все подписываемые схемы, включая ответы и события.
func (m Manifest) SchemaPaths() []string {
	var result []string
	for _, ops := range []map[string]string{m.Commands, m.Queries} {
		for _, p := range ops {
			result = append(result, p)
		}
	}
	if m.Contracts != nil {
		for _, ops := range []map[string]Operation{m.Contracts.Commands, m.Contracts.Queries} {
			for _, op := range ops {
				result = append(result, op.Result)
			}
		}
		for _, p := range m.Contracts.Events {
			result = append(result, p)
		}
	}
	return result
}
