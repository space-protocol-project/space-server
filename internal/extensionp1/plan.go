package extensionp1

import (
	"bytes"
	"encoding/json"
	"unicode"
	"unicode/utf8"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	pb "github.com/space-protocol-project/space-server/gen/space/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

type MutationResult struct {
	UI       string          `json:"ui_api"`
	Document json.RawMessage `json:"document"`
	Plan     json.RawMessage `json:"plan"`
}

// ParseMutation принимает ограниченный результат гостя; ничего не записывает.
func ParseMutation(raw []byte, revision uint64, events map[string]bool) ([]byte, *pb.ExtensionMutationPlan, error) {
	fail := status.Error(codes.InvalidArgument, "invalid_mutation_plan")
	if len(raw) > extensions.MaxJSON || extensions.CheckJSON(raw) != nil || !exactMutationFields(raw) {
		return nil, nil, fail
	}
	var result MutationResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || result.UI != "space.ui.p1" || extensions.CheckJSON(result.Document) != nil {
		return nil, nil, fail
	}
	var doc struct {
		UI string `json:"ui_api"`
	}
	if json.Unmarshal(result.Document, &doc) != nil || doc.UI != result.UI {
		return nil, nil, fail
	}
	plan := new(pb.ExtensionMutationPlan)
	if protojson.Unmarshal(result.Plan, plan) != nil || plan.ExpectedStateRevision != revision {
		return nil, nil, fail
	}
	if !validOperations(plan.Operations) || len(plan.Events) > 16 {
		return nil, nil, fail
	}
	for _, event := range plan.Events {
		if !events[event.Type] || extensions.CheckJSON(event.PayloadJson) != nil {
			return nil, nil, fail
		}
	}
	return bytes.Clone(result.Document), plan, nil
}

func exactMutationFields(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 3 {
		return false
	}
	for _, key := range []string{"ui_api", "document", "plan"} {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func validOperations(ops []*pb.ExtensionMutationOperation) bool {
	if len(ops) < 1 || len(ops) > 32 {
		return false
	}
	seen := make(map[string]bool)
	for _, op := range ops {
		if !validStorageKey(op.Key) || seen[op.Key] {
			return false
		}
		seen[op.Key] = true
		switch change := op.Change.(type) {
		case *pb.ExtensionMutationOperation_PutJson:
			if extensions.CheckJSON(change.PutJson) != nil {
				return false
			}
		case *pb.ExtensionMutationOperation_Delete:
			if !change.Delete {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validStorageKey(key string) bool {
	if len(key) < 1 || len(key) > 256 || !utf8.ValidString(key) {
		return false
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
