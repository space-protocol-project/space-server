package postgres

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	extensions "github.com/space-protocol-project/space-server/experiments/extensions"
)

const migrationDataVersion = "space.migration.data.p1"

type migrationData struct {
	Version string                     `json:"version"`
	Schema  uint32                     `json:"schema"`
	Values  map[string]json.RawMessage `json:"values"`
}

func decodeMigrationData(raw []byte, schema uint32) (migrationData, error) {
	var d migrationData
	if extensions.CheckJSON(raw) != nil {
		return d, upgradeError()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&d) != nil || d.Version != migrationDataVersion || d.Schema != schema || d.Values == nil || len(d.Values) > 32 {
		return d, upgradeError()
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	if len(keys) != 3 || keys["version"] == nil || keys["schema"] == nil || keys["values"] == nil {
		return d, upgradeError()
	}
	for key, value := range d.Values {
		if !channelIDPattern.MatchString(key) || extensions.CheckJSON(value) != nil {
			return d, upgradeError()
		}
	}
	return d, nil
}

func readMigrationData(ctx context.Context, tx pgx.Tx, installation, instance string, schema uint32) ([]byte, error) {
	d := migrationData{Version: migrationDataVersion, Schema: schema, Values: map[string]json.RawMessage{}}
	rows, err := tx.Query(ctx, `SELECT key,value FROM extension_p1_data WHERE installation=$1 AND instance=$2 ORDER BY key`, installation, instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var value []byte
		if err = rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		d.Values[key] = value
		if len(d.Values) > 32 {
			return nil, upgradeError()
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	_, err = decodeMigrationData(raw, schema)
	return raw, err
}

func writeMigrationData(ctx context.Context, tx pgx.Tx, installation, instance string, d migrationData) error {
	if _, err := tx.Exec(ctx, `DELETE FROM extension_p1_data WHERE installation=$1 AND instance=$2`, installation, instance); err != nil {
		return err
	}
	for key, value := range d.Values {
		if _, err := tx.Exec(ctx, `INSERT INTO extension_p1_data(installation,instance,key,value) VALUES($1,$2,$3,$4)`, installation, instance, key, []byte(value)); err != nil {
			return err
		}
	}
	return nil
}
