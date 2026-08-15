package migration

import (
	"fmt"

	"cpa-usage-keeper/internal/entities"

	"gorm.io/gorm"
)

func createQuotaObservationsMigration(db *gorm.DB) error {
	if err := db.AutoMigrate(&entities.QuotaObservation{}); err != nil {
		return fmt.Errorf("create quota observations table: %w", err)
	}
	return nil
}
