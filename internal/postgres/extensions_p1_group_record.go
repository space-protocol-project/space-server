package postgres

import (
	"bytes"
	"encoding/json"

	"github.com/space-protocol-project/space-server/experiments/extensions/upgrade"
)

// ExtensionP1GroupMember связывает план с конкретным экземпляром. Порядок
// лексикографический; состав установки проверяется целиком, без подмножеств.
type ExtensionP1GroupMember struct {
	Instance string
	Plan     upgrade.Plan
	Target   ExtensionP1MutationBinding
	Modules  map[string][]byte
}

// ExtensionP1GroupEvidence содержит свидетельства для каждого экземпляра.
// Доверенный адаптер дополнительно подтверждает общий digest состава.
type ExtensionP1GroupEvidence struct {
	Digest       string
	Verification upgrade.Verification
	Checkpoint   upgrade.Checkpoint
}

type groupRecord struct {
	Format  int
	Members []versionRecord
}

// ExtensionP1GroupProgress не выдаёт данные и исполняемые артефакты.
type ExtensionP1GroupProgress struct {
	Digest           string
	NextSteps        map[string]int
	Ready, Published bool
}

func (g groupRecord) digest() string {
	type member struct {
		Instance string
		Plan     upgrade.Plan
	}
	members := make([]member, len(g.Members))
	for i, r := range g.Members {
		members[i] = member{r.Instance, r.Plan}
	}
	raw, _ := json.Marshal(members)
	return upgradeHash(append([]byte("space.upgrade.group.p1\x00"), raw...))
}

func (g groupRecord) progress(published bool) ExtensionP1GroupProgress {
	p := ExtensionP1GroupProgress{Digest: g.digest(), NextSteps: map[string]int{}, Ready: true, Published: published}
	for _, r := range g.Members {
		p.NextSteps[r.Instance] = len(r.Steps)
		p.Ready = p.Ready && len(r.Steps) == len(r.Plan.Steps)
	}
	return p
}

func validateGroupMembers(g groupRecord) error {
	if g.Format != 1 || len(g.Members) < 2 || len(g.Members) > 8 {
		return upgradeError()
	}
	first := g.Members[0].Plan
	previous := ""
	for _, r := range g.Members {
		p := r.Plan
		if !channelIDPattern.MatchString(r.Instance) || r.Instance <= previous || p.Validate() != nil {
			return upgradeError()
		}
		if p.Space != first.Space || p.Installation != first.Installation || p.Generation != first.Generation || p.Revision != first.Revision || p.Source != first.Source || p.Target != first.Target || p.SchemaFrom != first.SchemaFrom || p.SchemaTo != first.SchemaTo || p.Grants != first.Grants || p.Checkpoint != first.Checkpoint {
			return upgradeError()
		}
		if err := validateMigrationArtifacts(p, r.Artifacts); err != nil {
			return err
		}
		previous = r.Instance
	}
	return nil
}

func (g groupRecord) validate(published bool) error {
	if err := validateGroupMembers(g); err != nil {
		return err
	}
	for _, r := range g.Members {
		if err := validateGroupProgress(r, published); err != nil {
			return err
		}
	}
	return nil
}

func validateGroupProgress(r versionRecord, published bool) error {
	if published {
		return r.validate()
	}
	if r.Format != 1 || r.PreparedBy == "" || r.PublishedBy != "" || r.Generation != "" || len(r.Steps) > len(r.Plan.Steps) {
		return upgradeError()
	}
	if _, err := decodeMigrationData(r.Seed, r.Plan.SchemaFrom); err != nil {
		return err
	}
	previous := r.Seed
	for i, step := range r.Steps {
		plan := r.Plan.Steps[i]
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
	return nil
}

func decodeGroup(raw []byte, hash string, published bool) (groupRecord, error) {
	var g groupRecord
	if len(raw) > 40000000 || upgradeHash(raw) != hash || json.Unmarshal(raw, &g) != nil {
		return g, upgradeError()
	}
	return g, g.validate(published)
}

// ExtensionP1GroupDigest позволяет доверенному адаптеру привязать разрешение
// и резервную копию к полному упорядоченному составу обновления.
func ExtensionP1GroupDigest(members []ExtensionP1GroupMember) (string, error) {
	g := groupRecord{Format: 1}
	for _, m := range members {
		g.Members = append(g.Members, versionRecord{Instance: m.Instance, Plan: m.Plan, Artifacts: migrationArtifacts{Target: m.Target, Modules: m.Modules}})
	}
	if err := validateGroupMembers(g); err != nil {
		return "", err
	}
	return g.digest(), nil
}
