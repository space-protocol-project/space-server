package demo

import (
	"encoding/json"

	"github.com/space-protocol-project/space-server/experiments/extensions/valueschema"
)

// IsMutation читает эффект только из проверенного неизменяемого manifest.
func (d *Dispatcher) IsMutation(instance, command string) bool {
	r, ok := d.registrations[instance]
	if !ok || len(r.SourceManifest) == 0 {
		return false
	}
	m, err := supportedManifest(r.SourceManifest)
	return err == nil && m.Contracts != nil && m.Contracts.Commands[command].Effect == "mutation"
}

func validateResult(r Registration, request Request, output []byte) error {
	if len(r.SourceManifest) == 0 {
		return nil
	}
	m, err := supportedManifest(r.SourceManifest)
	if err != nil {
		return err
	}
	if m.Contracts == nil {
		return nil
	}
	op := m.Contracts.Queries[request.Name]
	if request.Kind == "command" {
		op = m.Contracts.Commands[request.Name]
	}
	document := output
	if op.Effect == "mutation" {
		var envelope struct {
			Document json.RawMessage `json:"document"`
		}
		if json.Unmarshal(output, &envelope) != nil {
			return ErrExtension
		}
		document = envelope.Document
	}
	schema, err := valueschema.Decode(r.SourceSchemas[op.Result])
	if err != nil || schema.Validate(document) != nil {
		return ErrExtension
	}
	return nil
}
