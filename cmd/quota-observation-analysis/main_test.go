package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRunAnalyzesOnlyAggregateQuotaHistoryWithoutWriting(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "app.db")
	seedAggregateAnalysisDatabase(t, databasePath)
	before := fileDigest(t, databasePath)
	analysisTime := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(
		[]string{"--db", databasePath, "--now", analysisTime.Format(time.RFC3339Nano)},
		&stdout,
		&stderr,
		func() time.Time { return time.Time{} },
	)
	if exitCode != 0 {
		t.Fatalf("run exit code = %d, stderr=%s", exitCode, stderr.String())
	}
	after := fileDigest(t, databasePath)
	if before != after {
		t.Fatal("read-only aggregate analysis changed app.db")
	}
	var report aggregateReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout.String())
	}
	if report.AnalysisVersion != analysisVersion ||
		!report.TablePresent ||
		report.ObservationRows != 4 ||
		report.DistinctCredentials != 2 ||
		report.DistinctWindowKinds != 2 ||
		report.DistinctResetEpochs != 3 ||
		report.ActiveRecordingDays != 2 {
		t.Fatalf("unexpected aggregate counts: %+v", report)
	}
	if report.RowsLast24Hours != 2 ||
		report.RowsLast7Days != 4 ||
		report.RowsLast30Days != 4 {
		t.Fatalf("unexpected recording windows: %+v", report)
	}
	if report.NullAttributedTokenRows != 1 ||
		report.ZeroAttributedTokenRows != 1 ||
		report.PositiveAttributedTokenRows != 2 {
		t.Fatalf("unexpected attribution aggregates: %+v", report)
	}
	if report.EstimateCount != 3 ||
		report.ConfidenceCounts["insufficient"] != 3 ||
		report.PointClassCounts["included"] != 1 ||
		report.PointClassCounts["pricing_excluded"] != 3 {
		t.Fatalf("unexpected estimator aggregates: %+v", report)
	}
	if report.EarliestObservation == nil ||
		*report.EarliestObservation != "2026-07-22T10:00:00Z" ||
		report.LatestObservation == nil ||
		*report.LatestObservation != "2026-07-24T11:00:00Z" {
		t.Fatalf("unexpected aggregate range: %+v", report)
	}
	if report.DatabaseSizeBytes <= 0 || report.DatabaseAllocatedBytes <= 0 {
		t.Fatalf("database size aggregates were not measured: %+v", report)
	}
}

func TestAnalyzeMissingQuotaObservationTableReportsZeroWithoutCreatingIt(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "app.db")
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	if err := db.Exec("CREATE TABLE unrelated (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create unrelated table: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access seed pool: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close seed pool: %v", err)
	}
	before := fileDigest(t, databasePath)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(
		[]string{"--db", databasePath, "--now", "2026-07-24T12:00:00Z"},
		&stdout,
		&stderr,
		time.Now,
	)
	if exitCode != 0 {
		t.Fatalf("run exit code = %d, stderr=%s", exitCode, stderr.String())
	}
	var report aggregateReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.TablePresent || report.ObservationRows != 0 {
		t.Fatalf("missing table report = %+v, want zero-row result", report)
	}
	if before != fileDigest(t, databasePath) {
		t.Fatal("missing-table analysis changed app.db")
	}
}

func seedAggregateAnalysisDatabase(t *testing.T, databasePath string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	if err := db.Exec(`
CREATE TABLE quota_observations (
	id INTEGER PRIMARY KEY,
	usage_identity_id INTEGER NOT NULL,
	auth_type TEXT NOT NULL,
	auth_index TEXT NOT NULL,
	provider TEXT NOT NULL,
	window_kind_id TEXT NOT NULL,
	window_seconds INTEGER NOT NULL,
	observed_at TEXT NOT NULL,
	used_percent REAL,
	reset_at TEXT,
	reset_raw TEXT,
	attributed_tokens INTEGER
)`).Error; err != nil {
		t.Fatalf("create quota observations: %v", err)
	}
	if err := db.Exec(`
INSERT INTO quota_observations
	(id, usage_identity_id, auth_type, auth_index, provider, window_kind_id, window_seconds, observed_at, used_percent, reset_at, reset_raw, attributed_tokens)
VALUES
	(1, 1, 'oauth', 'credential-1', 'codex', 'codex/overall/rate_limit/18000', 18000, '2026-07-22T10:00:00Z', 10, '2026-07-22T15:00:00Z', '2026-07-22T15:00:00Z', NULL),
	(2, 1, 'oauth', 'credential-1', 'codex', 'codex/overall/rate_limit/18000', 18000, '2026-07-22T11:00:00Z', 15, '2026-07-22T15:00:00Z', '2026-07-22T15:00:00Z', 0),
	(3, 1, 'oauth', 'credential-1', 'codex', 'codex/overall/rate_limit/604800', 604800, '2026-07-24T10:00:00Z', 20, '2026-07-30T10:00:00Z', '2026-07-30T10:00:00Z', 100),
	(4, 2, 'oauth', 'credential-2', 'codex', 'codex/overall/rate_limit/18000', 18000, '2026-07-24T11:00:00Z', 25, '2026-07-24T15:00:00Z', '2026-07-24T15:00:00Z', 200)
`).Error; err != nil {
		t.Fatalf("seed quota observations: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access seed pool: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close seed pool: %v", err)
	}
}

func fileDigest(t *testing.T, path string) [sha256.Size]byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return sha256.Sum256(content)
}
