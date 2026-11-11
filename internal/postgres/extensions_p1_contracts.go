package postgres

import (
	"github.com/space-protocol-project/space-server/experiments/extensions/manifest"
	"github.com/space-protocol-project/space-server/experiments/extensions/valueschema"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
)

func (b *extensionMutationBinding) bindContracts(source ExtensionP1MutationBinding) error {
	if len(source.Registration.SourceManifest) == 0 {
		if len(source.Capabilities) != 0 {
			return mutationError(codes.InvalidArgument, "invalid_capabilities")
		}
		return nil
	}
	m, err := manifest.Decode(source.Registration.SourceManifest)
	if err != nil || len(source.Events) != 0 || !approvedCapabilities(m.Permissions, source.Capabilities) {
		return mutationError(codes.InvalidArgument, "invalid_manifest_binding")
	}
	b.command = ""
	b.contracts = m.Contracts
	b.schemas = source.Registration.SourceSchemas
	return nil
}

func approvedCapabilities(required, approved []string) bool {
	if len(required) != len(approved) {
		return false
	}
	grants := map[string]bool{}
	for _, grant := range approved {
		if grants[grant] || (grant != "storage.own" && grant != "events.own") {
			return false
		}
		grants[grant] = true
	}
	for _, grant := range required {
		if !grants[grant] {
			return false
		}
	}
	return true
}

func (b extensionMutationBinding) handles(command string) bool {
	if command == "" {
		return false
	}
	if b.contracts != nil {
		return b.contracts.Commands[command].Effect == "mutation"
	}
	return b.command == command
}

func (b extensionMutationBinding) allowedEvents(command string) map[string]bool {
	if b.contracts == nil {
		return b.events
	}
	result := map[string]bool{}
	for _, event := range b.contracts.Commands[command].Events {
		result[event] = true
	}
	return result
}

func (b extensionMutationBinding) validateEvents(events []*pb.ExtensionMutationEvent) error {
	if b.contracts == nil {
		return nil
	}
	for _, event := range events {
		schema, err := valueschema.Decode(b.schemas[b.contracts.Events[event.Type]])
		if err != nil || schema.Validate(event.PayloadJson) != nil {
			return mutationError(codes.InvalidArgument, "invalid_event_payload")
		}
	}
	return nil
}
