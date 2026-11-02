// Package upgrade проверяет привязку плана обновления P1 к свидетельствам ядра.
// Пакет не проверяет подписи и не исполняет миграции или SQL.
package upgrade

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"

	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
	"github.com/space-protocol-project/space-server/experiments/extensions/lifecycle"
)

const Version = "space.upgrade.p1"

var ErrPlan = errors.New("upgrade_invalid_plan")
var ErrEvidence = errors.New("upgrade_invalid_evidence")
var ErrAuthority = errors.New("upgrade_authority")
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var generationPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Step struct {
	ID     string `json:"id"`
	From   uint32 `json:"from"`
	To     uint32 `json:"to"`
	Digest string `json:"digest"`
}

type Plan struct {
	Version      string `json:"version"`
	Space        string `json:"space"`
	Installation string `json:"installation"`
	Generation   string `json:"generation"`
	Revision     int64  `json:"revision"`
	DataRevision uint64 `json:"data_revision"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	Grants       string `json:"grants"`
	Checkpoint   string `json:"checkpoint"`
	SchemaFrom   uint32 `json:"schema_from"`
	SchemaTo     uint32 `json:"schema_to"`
	Steps        []Step `json:"steps"`
}

// Decode отклоняет дубли ключей, лишние поля, пропуски и нецелые числа.
func Decode(raw []byte) (Plan, error) {
	var p Plan
	if extensions.CheckJSON(raw) != nil {
		return p, ErrPlan
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil {
		return Plan{}, ErrPlan
	}
	if err := p.Validate(); err != nil {
		return Plan{}, err
	}
	if err := exactFields(raw); err != nil {
		return Plan{}, err
	}
	return p, nil
}

func requiredFields(raw []byte, names []string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(names) {
		return nil, ErrPlan
	}
	for _, name := range names {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, ErrPlan
		}
	}
	return fields, nil
}

// encoding/json сопоставляет ключи без учёта регистра; профиль P1 этого не допускает.
func exactFields(raw []byte) error {
	fields, err := requiredFields(raw, []string{"version", "space", "installation", "generation", "revision", "data_revision", "source", "target", "grants", "checkpoint", "schema_from", "schema_to", "steps"})
	if err != nil {
		return err
	}
	var steps []json.RawMessage
	if json.Unmarshal(fields["steps"], &steps) != nil {
		return ErrPlan
	}
	for _, step := range steps {
		if _, err := requiredFields(step, []string{"id", "from", "to", "digest"}); err != nil {
			return err
		}
	}
	return nil
}

func (p Plan) Validate() error {
	if p.Version != Version || !namePattern.MatchString(p.Space) || !namePattern.MatchString(p.Installation) || !generationPattern.MatchString(p.Generation) {
		return ErrPlan
	}
	if p.Revision < 1 || p.Revision > lifecycle.MaxRevision-2 || p.DataRevision > 9007199254740991 {
		return ErrPlan
	}
	if !hashPattern.MatchString(p.Source) || !hashPattern.MatchString(p.Target) || !hashPattern.MatchString(p.Grants) || !hashPattern.MatchString(p.Checkpoint) || p.Source == p.Target {
		return ErrPlan
	}
	if p.SchemaFrom < 1 || p.SchemaTo < p.SchemaFrom || p.SchemaTo > 65535 || p.Steps == nil || len(p.Steps) > 32 {
		return ErrPlan
	}
	return p.validateSteps()
}

func (p Plan) validateSteps() error {
	current := p.SchemaFrom
	names := map[string]bool{}
	for _, step := range p.Steps {
		if !namePattern.MatchString(step.ID) || names[step.ID] || !hashPattern.MatchString(step.Digest) {
			return ErrPlan
		}
		if step.From != current || step.To != current+1 {
			return ErrPlan
		}
		names[step.ID] = true
		current = step.To
	}
	if current != p.SchemaTo {
		return ErrPlan
	}
	return nil
}

// Digest использует нормализованный JSON структуры, а не байты входного JSON.
// Порядок Steps значим; это профиль Go P1, не обещание общей канонизации JCS.
func (p Plan) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", ErrPlan
	}
	sum := sha256.Sum256(append([]byte(Version+"\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}
