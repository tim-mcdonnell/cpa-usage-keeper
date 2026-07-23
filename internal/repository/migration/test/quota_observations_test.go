package test

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"cpa-usage-keeper/internal/config"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/migration"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const quotaObservationsMigrationVersion = "20260723_quota_observations"

type quotaObservationColumn struct {
	Name       string
	Type       string
	NotNull    int `gorm:"column:notnull"`
	PrimaryKey int `gorm:"column:pk"`
}

type quotaObservationIndex struct {
	Name   string
	Unique int
}

func TestQuotaObservationsFreshUpgradeAndIdempotentSchemas(t *testing.T) {
	fresh, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "fresh.db")})
	if err != nil {
		t.Fatalf("open fresh database: %v", err)
	}
	closeQuotaObservationMigrationDatabase(t, fresh)

	upgrade, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "upgrade.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open upgrade database: %v", err)
	}
	closeQuotaObservationMigrationDatabase(t, upgrade)
	if err := upgrade.AutoMigrate(withoutQuotaObservation(entities.All())...); err != nil {
		t.Fatalf("create pre-observation schema: %v", err)
	}
	if err := migration.MarkAllAsApplied(upgrade); err != nil {
		t.Fatalf("mark historical migrations: %v", err)
	}
	if err := upgrade.Exec("DELETE FROM schema_migrations WHERE version = ?", quotaObservationsMigrationVersion).Error; err != nil {
		t.Fatalf("enable quota observations migration: %v", err)
	}
	if err := migration.Run(upgrade); err != nil {
		t.Fatalf("run quota observations migration: %v", err)
	}

	assertQuotaObservationSchema(t, fresh)
	assertQuotaObservationSchema(t, upgrade)
	if got, want := describeQuotaObservationSchema(t, upgrade), describeQuotaObservationSchema(t, fresh); !reflect.DeepEqual(got, want) {
		t.Fatalf("upgrade schema differs from fresh schema\nupgrade: %+v\nfresh:   %+v", got, want)
	}

	if err := migration.Run(upgrade); err != nil {
		t.Fatalf("rerun quota observations migration: %v", err)
	}
	var applied int64
	if err := upgrade.Table("schema_migrations").Where("version = ?", quotaObservationsMigrationVersion).Count(&applied).Error; err != nil {
		t.Fatalf("count quota observations migration: %v", err)
	}
	if applied != 1 {
		t.Fatalf("expected migration version once, got %d", applied)
	}
}

func assertQuotaObservationSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	if !db.Migrator().HasTable(&entities.QuotaObservation{}) {
		t.Fatal("expected quota_observations table")
	}
	description := describeQuotaObservationSchema(t, db)
	columns := make(map[string]quotaObservationColumn, len(description.columns))
	for _, column := range description.columns {
		columns[column.Name] = column
	}
	for _, name := range []string{
		"usage_identity_id",
		"auth_type",
		"auth_index",
		"provider",
		"window_kind_id",
		"quota_key",
		"scope",
		"group_key",
		"window_role",
		"observed_at",
		"source",
		"percent_source",
		"attributed_cost_complete",
		"pricing_snapshot_hash",
		"created_at",
	} {
		if columns[name].NotNull != 1 {
			t.Errorf("expected %s to be NOT NULL, got %+v", name, columns[name])
		}
	}
	for _, forbidden := range []string{"prompt", "response", "credential", "token_material", "raw_json", "percent_resolution"} {
		if _, exists := columns[forbidden]; exists {
			t.Fatalf("quota observations must not persist %s", forbidden)
		}
	}
	for _, name := range []string{
		"used_percent",
		"remaining_fraction",
		"used",
		"limit_value",
		"remaining",
		"reset_at",
		"reset_raw",
		"attributed_tokens",
		"attributed_cost_usd",
	} {
		if columns[name].NotNull != 0 {
			t.Errorf("expected %s to remain nullable, got %+v", name, columns[name])
		}
	}
	indexes := make(map[string]quotaObservationIndex, len(description.indexes))
	for _, index := range description.indexes {
		indexes[index.Name] = index
	}
	for _, name := range []string{"idx_quota_observations_identity_window_time", "idx_quota_observations_observed_at"} {
		index, ok := indexes[name]
		if !ok || index.Unique != 0 {
			t.Errorf("expected non-unique index %s, got %+v", name, index)
		}
	}
}

type quotaObservationSchemaDescription struct {
	columns []quotaObservationColumn
	indexes []quotaObservationIndex
}

func describeQuotaObservationSchema(t *testing.T, db *gorm.DB) quotaObservationSchemaDescription {
	t.Helper()
	var columns []quotaObservationColumn
	if err := db.Raw("PRAGMA table_info('quota_observations')").Scan(&columns).Error; err != nil {
		t.Fatalf("describe quota_observations columns: %v", err)
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].Name < columns[j].Name })
	var indexes []quotaObservationIndex
	if err := db.Raw("PRAGMA index_list('quota_observations')").Scan(&indexes).Error; err != nil {
		t.Fatalf("list quota_observations indexes: %v", err)
	}
	sort.Slice(indexes, func(i, j int) bool { return indexes[i].Name < indexes[j].Name })
	return quotaObservationSchemaDescription{columns: columns, indexes: indexes}
}

func withoutQuotaObservation(models []any) []any {
	filtered := make([]any, 0, len(models)-1)
	for _, model := range models {
		if _, ok := model.(*entities.QuotaObservation); ok {
			continue
		}
		filtered = append(filtered, model)
	}
	return filtered
}

func closeQuotaObservationMigrationDatabase(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
}
