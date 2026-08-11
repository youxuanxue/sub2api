package pgpartition

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// InventoryChildBound describes one direct child partition for read-only inventory.
type InventoryChildBound struct {
	Schema         string
	Name           string
	BoundExpr      string
	Lower          time.Time
	Upper          time.Time
	LowerUnbounded bool
	UpperUnbounded bool
	IsDefault      bool
	Layout         string
}

// ListInventoryChildBounds returns direct child bounds without failing on legacy layouts.
func ListInventoryChildBounds(ctx context.Context, db DB, table string) ([]InventoryChildBound, error) {
	rows, err := db.QueryContext(ctx, childBoundsQuery, table)
	if err != nil {
		return nil, fmt.Errorf("pgpartition: list inventory child bounds of %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var out []InventoryChildBound
	for rows.Next() {
		child, lower, upper, err := scanChildBoundRow(rows)
		if err != nil {
			return nil, err
		}
		inv := InventoryChildBound{
			Schema:         child.Schema,
			Name:           child.Name,
			BoundExpr:      child.BoundExpr,
			LowerUnbounded: child.LowerUnbounded,
			UpperUnbounded: child.UpperUnbounded,
			IsDefault:      child.IsDefault,
			Layout:         inventoryLayout(child, lower, upper),
		}
		if !child.IsDefault && lower.Valid && upper.Valid {
			inv.Lower = lower.Time
			inv.Upper = upper.Time
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgpartition: iterate inventory child bounds: %w", err)
	}
	return out, nil
}

func inventoryLayout(child ChildPartitionBound, lower, upper sql.NullTime) string {
	if child.IsDefault {
		return "default"
	}
	if strings.Contains(child.Name, "rehome") && strings.Contains(child.Name, "staging") {
		return "legacy_staging"
	}
	if !lower.Valid || !upper.Valid || child.LowerUnbounded || child.UpperUnbounded {
		return "legacy"
	}
	if upper.Time.Sub(lower.Time) == time.Hour && isCanonicalHourlyName(child.Name, lower.Time) {
		return "hourly"
	}
	if d := upper.Time.Sub(lower.Time); d >= 24*time.Hour && d < 32*24*time.Hour {
		return "monthly"
	}
	return "legacy"
}

func isCanonicalHourlyName(name string, lower time.Time) bool {
	want := HourlyPartitionName(qaRecordsTableName, lower)
	return name == want
}
