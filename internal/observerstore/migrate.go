// Package observerstore owns observer identity/evidence history. It does not
// provide an HTTP receiver or payment processing authority.
package observerstore

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed 001_identity.sql
var schemaOne string

var ErrStorage = errors.New("observer_storage")
var ErrSchema = errors.New("observer_schema_mismatch")
var ErrScope = errors.New("observer_scope_mismatch")
var ErrInput = errors.New("observer_invalid_input")

// Migrate is additive and atomic. Unknown/drifted versions fail without repair.
func Migrate(ctx context.Context, db *gorm.DB) error {
	err := db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(current_schema() || ':observer-migrations', 0))`).Error; err != nil { return err }
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS observer_schema_versions (version integer PRIMARY KEY, checksum text NOT NULL)`).Error; err != nil { return err }
		var versions []struct { Version int; Checksum string }
		if err := tx.Raw(`SELECT version, checksum FROM observer_schema_versions ORDER BY version`).Scan(&versions).Error; err != nil { return err }
		digest := sha256.Sum256([]byte(schemaOne))
		checksum := hex.EncodeToString(digest[:])
		if len(versions) != 0 {
			if len(versions) != 1 || versions[0].Version != 1 || versions[0].Checksum != checksum { return ErrSchema }
			return nil
		}
		if err := tx.Exec(schemaOne).Error; err != nil { return err }
		return tx.Exec(`INSERT INTO observer_schema_versions(version, checksum) VALUES (1, ?)`, checksum).Error
	})
	if errors.Is(err, ErrSchema) { return ErrSchema }
	if err != nil { return ErrStorage }
	return nil
}
