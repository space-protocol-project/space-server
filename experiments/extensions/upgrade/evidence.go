package upgrade

import "github.com/space-protocol-project/space-server/experiments/extensions/lifecycle"

// Binding читается ядром из согласованного состояния, не из запроса клиента.
type Binding struct {
	Space, Installation, Generation, Package string
	Revision                                 int64
	DataRevision                             uint64
	Schema                                   uint32
}

// Verification — результат внешних проверок подписи/доверия, совместимости
// и разрешений. Этот прототип лишь связывает результат с точным планом.
type Verification struct {
	Plan, Target, Grants          string
	Trusted, Compatible, Approved bool
}

type Checkpoint struct {
	Plan, Digest, Source string
	Schema               uint32
	DataRevision         uint64
	Complete             bool
}

type Migration struct {
	Plan, Checkpoint, Target, Result string
	Schema                           uint32
	Validated                        bool
}

func (p Plan) matches(b Binding, revision int64) bool {
	return b.Space == p.Space && b.Installation == p.Installation && b.Generation == p.Generation && b.Revision == revision && b.Package == p.Source && b.Schema == p.SchemaFrom && b.DataRevision == p.DataRevision
}

func (p Plan) evidence(v Verification, c Checkpoint) (string, error) {
	digest, err := p.Digest()
	if err != nil {
		return "", err
	}
	if v.Plan != digest || v.Target != p.Target || v.Grants != p.Grants || !v.Trusted || !v.Compatible || !v.Approved {
		return "", ErrEvidence
	}
	if c.Plan != digest || c.Digest != p.Checkpoint || c.Source != p.Source || c.Schema != p.SchemaFrom || c.DataRevision != p.DataRevision || !c.Complete {
		return "", ErrEvidence
	}
	return digest, nil
}

// Begin не меняет реестр. Координатор дополнительно проверяет disabled и Active=0;
// адаптер обязан сериализовать проверку свидетельств с применением перехода.
func Begin(p Plan, b Binding, v Verification, c Checkpoint, ownerAuthorized bool) (lifecycle.Observation, error) {
	if !ownerAuthorized {
		return lifecycle.Observation{}, ErrAuthority
	}
	if _, err := p.evidence(v, c); err != nil {
		return lifecycle.Observation{}, err
	}
	if !p.matches(b, p.Revision) {
		return lifecycle.Observation{}, ErrEvidence
	}
	return lifecycle.Observation{Version: lifecycle.Version, Action: "begin_upgrade", Actor: "owner", Authorized: true, ExpectedRevision: p.Revision, PackageVerified: true, Compatible: true, GrantsApproved: true, BackupReady: true}, nil
}

// Finish предполагает, что рабочая привязка ещё исходная, а результат миграции
// подготовлен отдельно. Публикация кода/данных и запись аудита — обязанность ядра.
func Finish(p Plan, b Binding, v Verification, c Checkpoint, m Migration) (lifecycle.Observation, error) {
	digest, err := p.evidence(v, c)
	if err != nil {
		return lifecycle.Observation{}, err
	}
	if !p.matches(b, p.Revision+1) {
		return lifecycle.Observation{}, ErrEvidence
	}
	if m.Plan != digest || m.Checkpoint != c.Digest || m.Target != p.Target || m.Schema != p.SchemaTo || !hashPattern.MatchString(m.Result) || !m.Validated {
		return lifecycle.Observation{}, ErrEvidence
	}
	return lifecycle.Observation{Version: lifecycle.Version, Action: "finish_upgrade", Actor: "system", Authorized: true, ExpectedRevision: p.Revision + 1, PackageVerified: true, Compatible: true, GrantsApproved: true, MigrationReady: true}, nil
}
