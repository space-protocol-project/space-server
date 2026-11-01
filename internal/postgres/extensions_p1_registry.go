package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/space-protocol-project/space-server/experiments/extensions/lifecycle"
	"google.golang.org/grpc/codes"
)

const extensionRegistrySchema = `CREATE TABLE extension_p1_installations (
 installation text PRIMARY KEY,
 binding_hash bytea NOT NULL CHECK(octet_length(binding_hash)=32),
 state text NOT NULL CHECK(state IN ('disabled','enabled','disabling','quarantined','removed')),
 revision bigint NOT NULL CHECK(revision BETWEEN 1 AND 2147483646),
 generation text NOT NULL CHECK(generation ~ '^[0-9a-f]{32}$')
); UPDATE extension_p1_meta SET lifecycle_version=1`

// Захват нового поколения атомарен для всего набора установок. Старые
// оболочки теряют право на COMMIT, даже если их процесс ещё не завершился.
func (c *extensionLifecycle) restore(ctx context.Context, tx pgx.Tx, bindings map[string]extensionMutationBinding) error {
	var err error
	var version int
	if err = tx.QueryRow(ctx, `SELECT lifecycle_version FROM extension_p1_meta WHERE singleton FOR UPDATE`).Scan(&version); err != nil {
		return err
	}
	if version == 0 {
		if _, err = tx.Exec(ctx, extensionRegistrySchema); err != nil {
			return err
		}
	} else if version != 1 {
		return mutationError(codes.FailedPrecondition, "lifecycle_storage_version")
	}
	names := make([]string, 0, len(c.installations))
	for name := range c.installations {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err = c.restoreInstallation(ctx, tx, name, lifecycleBindingHash(bindings, name)); err != nil {
			return err
		}
	}
	return nil
}

func lifecycleBindingHash(bindings map[string]extensionMutationBinding, installation string) [32]byte {
	rows := map[string]any{}
	for instance, b := range bindings {
		if b.installation == installation {
			rows[instance] = []any{b.channel, b.packageHash}
		}
	}
	raw, _ := json.Marshal(rows)
	return sha256.Sum256(raw)
}

func (c *extensionLifecycle) restoreInstallation(ctx context.Context, tx pgx.Tx, name string, hash [32]byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO extension_p1_installations(installation,binding_hash,state,revision,generation)
 VALUES($1,$2,'disabled',1,$3) ON CONFLICT DO NOTHING`, name, hash[:], c.generation)
	if err != nil {
		return err
	}
	var state string
	var revision int64
	err = tx.QueryRow(ctx, `UPDATE extension_p1_installations SET generation=$3 WHERE installation=$1 AND binding_hash=$2 RETURNING state,revision`, name, hash[:], c.generation).Scan(&state, &revision)
	if err == pgx.ErrNoRows {
		return mutationError(codes.FailedPrecondition, "lifecycle_binding_changed")
	}
	if err != nil {
		return err
	}
	coordinator, err := lifecycle.New(state, revision)
	if err != nil {
		return err
	}
	c.installations[name].coordinator = coordinator
	return nil
}

func (c *extensionLifecycle) persist(ctx context.Context, tx pgx.Tx, installation string, revision int64, d lifecycle.Decision) error {
	tag, err := tx.Exec(ctx, `UPDATE extension_p1_installations SET state=$4,revision=$5
 WHERE installation=$1 AND generation=$2 AND revision=$3`, installation, c.generation, revision, d.State, d.Revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return mutationError(codes.FailedPrecondition, "lifecycle_fenced")
	}
	return nil
}

// FOR SHARE удерживается транзакцией мутации до COMMIT: новое поколение и
// переход состояния не могут пересечь фиксацию уже проверенной команды.
func (c *extensionLifecycle) checkCommit(ctx context.Context, tx pgx.Tx, installation string) error {
	if c == nil {
		var managed bool
		err := tx.QueryRow(ctx, `SELECT COALESCE((to_jsonb(m)->>'lifecycle_version')::int,0)<>0 FROM extension_p1_meta m WHERE singleton`).Scan(&managed)
		if err != nil {
			return err
		}
		if managed {
			return mutationError(codes.FailedPrecondition, "managed_service_required")
		}
		return nil
	}
	var generation, state string
	err := tx.QueryRow(ctx, `SELECT generation,state FROM extension_p1_installations WHERE installation=$1 FOR SHARE`, installation).Scan(&generation, &state)
	if err != nil {
		return err
	}
	if generation != c.generation || state != "enabled" {
		return mutationError(codes.FailedPrecondition, "lifecycle_fenced")
	}
	return nil
}

func (c *extensionLifecycle) checkGeneration(ctx context.Context, installation string) error {
	var generation string
	err := c.store.pool.QueryRow(ctx, `SELECT generation FROM extension_p1_installations WHERE installation=$1`, installation).Scan(&generation)
	if err != nil {
		return mutationDatabaseError(err)
	}
	if generation != c.generation {
		return mutationError(codes.FailedPrecondition, "lifecycle_fenced")
	}
	return nil
}

func (c *extensionLifecycle) systemChange(ctx context.Context, i *extensionLifecycleInstallation, installation, action string, revision int64) (lifecycle.Decision, error) {
	o := lifecycleObservation(action, "system", revision)
	d := i.coordinator.Preview(o)
	if d.Code != "OK" {
		return d, lifecycleDecisionError(d)
	}
	tx, err := c.store.pool.Begin(ctx)
	if err != nil {
		return lifecycle.Decision{}, mutationDatabaseError(err)
	}
	defer tx.Rollback(ctx)
	if err = c.persist(ctx, tx, installation, revision, d); err != nil {
		return lifecycle.Decision{}, mutationDatabaseError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		i.failed.Store(true)
		i.coordinator.Apply(lifecycleObservation("quarantine", "system", revision))
		return lifecycle.Decision{}, mutationDatabaseError(err)
	}
	return i.coordinator.Apply(o), nil
}
