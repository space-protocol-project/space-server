package postgres

import (
	"bytes"
	"encoding/json"

	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
)

type versionStep struct {
	Digest, Input, Generation, Principal string
	Output                               []byte
	WASM                                 bool
}

// Полная запись сохраняет проверяемый результат и артефакты каждого обновления.
// Начальный пакет до первой публикации известен только по исходному digest.
type versionRecord struct {
	Format                                        int
	Plan                                          upgrade.Plan
	Instance, PreparedBy, PublishedBy, Generation string
	Seed, Document                                []byte
	Artifacts                                     migrationArtifacts
	Steps                                         []versionStep
}

// ExtensionP1Version — ограниченные метаданные истории без выдачи WASM и данных.
type ExtensionP1Version struct {
	Sequence               int64
	Digest, Source, Target string
	SchemaFrom, SchemaTo   uint32
	Revision               int64
	Principal, Generation  string
}

type versionHead struct {
	Meta   ExtensionP1Version
	Record versionRecord
}

func (r versionRecord) validate() error {
	if r.Format != 1 || r.Plan.Validate() != nil || !channelIDPattern.MatchString(r.Instance) || r.PreparedBy == "" || r.PublishedBy == "" || !extensionHistoryPattern.MatchString(r.Generation) {
		return upgradeError()
	}
	if err := validateMigrationArtifacts(r.Plan, r.Artifacts); err != nil {
		return err
	}
	if _, err := decodeMigrationData(r.Seed, r.Plan.SchemaFrom); err != nil {
		return err
	}
	if len(r.Steps) != len(r.Plan.Steps) {
		return upgradeError()
	}
	previous := r.Seed
	for index, step := range r.Steps {
		plan := r.Plan.Steps[index]
		if !step.WASM || step.Digest != plan.Digest || step.Input != upgradeHash(previous) || step.Principal == "" || !extensionHistoryPattern.MatchString(step.Generation) {
			return upgradeError()
		}
		if _, err := decodeMigrationData(step.Output, plan.To); err != nil {
			return err
		}
		previous = step.Output
	}
	if !bytes.Equal(previous, r.Document) {
		return upgradeError()
	}
	_, err := decodeMigrationData(r.Document, r.Plan.SchemaTo)
	return err
}

func decodeVersionRecord(raw []byte, hash string) (versionRecord, error) {
	var r versionRecord
	if len(raw) > 5000000 || upgradeHash(raw) != hash || json.Unmarshal(raw, &r) != nil {
		return r, upgradeError()
	}
	return r, r.validate()
}

func validateVersionLink(previous *versionHead, next versionHead) error {
	p, m := next.Record.Plan, next.Meta
	digest, _ := p.Digest()
	if m.Digest != digest || m.Source != p.Source || m.Target != p.Target || m.SchemaFrom != p.SchemaFrom || m.SchemaTo != p.SchemaTo || m.Revision != p.Revision+1 || m.Principal != next.Record.PublishedBy || m.Generation != next.Record.Generation {
		return upgradeError()
	}
	if previous == nil {
		if m.Sequence != 1 || p.SchemaFrom != 1 {
			return upgradeError()
		}
		return nil
	}
	old := previous.Record
	if m.Sequence != previous.Meta.Sequence+1 || p.Source != old.Plan.Target || p.SchemaFrom != old.Plan.SchemaTo || p.Space != old.Plan.Space || next.Record.Instance != old.Instance || next.Record.Artifacts.Target.Channel != old.Artifacts.Target.Channel || p.Revision < previous.Meta.Revision || p.DataRevision < old.Plan.DataRevision {
		return upgradeError()
	}
	return nil
}
