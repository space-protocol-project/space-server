package demo

import (
	"context"
	"encoding/json"
	"strconv"
)

type trustedContext struct {
	Space    string `json:"space_id"`
	Instance string `json:"instance_id"`
	User     string `json:"principal_id"`
	Device   string `json:"device_id"`
	Revision string `json:"access_revision"`
	Deadline string `json:"deadline_unix_ms"`
}

func envelope(ctx context.Context, r Request, p Principal) []byte {
	deadline, _ := ctx.Deadline()
	value := struct {
		ABI     string                     `json:"abi"`
		Kind    string                     `json:"kind"`
		Name    string                     `json:"name"`
		Payload map[string]json.RawMessage `json:"payload"`
		Context trustedContext             `json:"context"`
	}{"space_plugin_p1", r.Kind, r.Name, r.Payload, trustedContext{p.Space, p.Instance, p.User, p.Device, p.Revision, strconv.FormatInt(deadline.UnixMilli(), 10)}}
	raw, _ := json.Marshal(value)
	return raw
}
