// Package lifecycle проверяет кандидат space.lifecycle.p1; это не рабочий реестр.
package lifecycle

const Version = "space.lifecycle.p1"
const MaxRevision = 2147483646

type Observation struct {
	Version          string `json:"version"`
	State            string `json:"state"`
	Action           string `json:"action"`
	Actor            string `json:"actor"`
	Authorized       bool   `json:"authorized"`
	Revision         int64  `json:"revision"`
	ExpectedRevision int64  `json:"expected_revision"`
	Active           int    `json:"active"`
	PackageVerified  bool   `json:"package_verified"`
	Compatible       bool   `json:"compatible"`
	GrantsApproved   bool   `json:"grants_approved"`
	BackupReady      bool   `json:"backup_ready"`
	MigrationReady   bool   `json:"migration_ready"`
}

type Decision struct {
	Code         string `json:"code"`
	State        string `json:"state"`
	Revision     int64  `json:"revision"`
	Admission    bool   `json:"admission"`
	CancelActive bool   `json:"cancel_active"`
	PreserveData bool   `json:"preserve_data"`
}

type rule struct {
	from         map[string]bool
	to, actor    string
	idle, cancel bool
	require      func(Observation) bool
}

func states(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

var knownStates = states("staged", "verified", "installed", "enabled", "disabling", "disabled", "upgrading", "failed", "quarantined", "removed")
var rules = map[string]rule{
	"verify":         {states("staged"), "verified", "system", true, false, verified},
	"install":        {states("verified"), "installed", "owner", true, false, runnable},
	"enable":         {states("installed", "disabled"), "enabled", "owner", true, false, runnable},
	"disable":        {states("enabled"), "disabling", "owner", false, true, nil},
	"finish_disable": {states("disabling"), "disabled", "system", true, false, nil},
	"begin_upgrade":  {states("disabled"), "upgrading", "owner", true, false, upgradeReady},
	"finish_upgrade": {states("upgrading"), "disabled", "system", true, false, migrationReady},
	"fail_upgrade":   {states("upgrading"), "failed", "system", false, true, nil},
	"quarantine":     {states("staged", "verified", "installed", "enabled", "disabling", "disabled", "upgrading", "failed"), "quarantined", "system", false, true, nil},
	"recover":        {states("failed", "quarantined"), "disabled", "owner", true, false, migrationReady},
	"remove":         {states("installed", "disabled", "failed", "quarantined"), "removed", "owner", true, false, nil},
}

func verified(o Observation) bool       { return o.PackageVerified }
func runnable(o Observation) bool       { return o.PackageVerified && o.Compatible && o.GrantsApproved }
func upgradeReady(o Observation) bool   { return runnable(o) && o.BackupReady }
func migrationReady(o Observation) bool { return runnable(o) && o.MigrationReady }

// Decide получает только наблюдение доверенного ядра. Поля свидетельств
// привязаны к выбранному digest/плану; их нельзя брать из запроса расширения.
func Decide(o Observation) Decision {
	d := Decision{Code: "InvalidArgument", State: o.State, Revision: o.Revision, PreserveData: true}
	r, ok := rules[o.Action]
	if !ok || o.Version != Version || !knownStates[o.State] || o.Revision < 1 || o.Revision > MaxRevision || o.ExpectedRevision < 1 || o.ExpectedRevision > MaxRevision || o.Active < 0 || o.Active > 128 {
		return d
	}
	if !o.Authorized || o.Actor != r.actor {
		d.Code = "PermissionDenied"
		return d
	}
	if o.ExpectedRevision != o.Revision {
		d.Code = "Aborted"
		return d
	}
	if !r.from[o.State] || (r.idle && o.Active != 0) || (r.require != nil && !r.require(o)) || o.Revision == MaxRevision {
		d.Code = "FailedPrecondition"
		return d
	}
	return Decision{"OK", r.to, o.Revision + 1, r.to == "enabled", r.cancel, true}
}
