package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"
)

type tablePartition struct {
	schema string
	name   string
	oid    uint32
}

func createPartitionedIndexConcurrently(ctx context.Context, db *sql.DB, policy nonTransactionalIndexPolicy) error {
	valid, err := qualifiedIndexIsValid(ctx, db, "public", policy.indexName)
	if err != nil {
		return err
	}
	if valid {
		return nil
	}

	partitions, err := listTablePartitions(ctx, db, policy.partitionedTable)
	if err != nil {
		return err
	}

	for _, partition := range partitions {
		childIndex := fmt.Sprintf("%s_p%d", policy.indexName, partition.oid)
		invalid, err := qualifiedIndexIsInvalid(ctx, db, partition.schema, childIndex)
		if err != nil {
			return err
		}
		qualifiedIndex := pq.QuoteIdentifier(partition.schema) + "." + pq.QuoteIdentifier(childIndex)
		if invalid {
			if _, err := db.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+qualifiedIndex); err != nil {
				return fmt.Errorf("drop invalid partition index %s: %w", childIndex, err)
			}
		}
		createDDL := fmt.Sprintf(
			"CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON %s.%s (%s)",
			pq.QuoteIdentifier(childIndex),
			pq.QuoteIdentifier(partition.schema),
			pq.QuoteIdentifier(partition.name),
			policy.partitionedIndexExpr,
		)
		if _, err := db.ExecContext(ctx, createDDL); err != nil {
			return fmt.Errorf("create partition index %s: %w", childIndex, err)
		}
	}

	parentDDL := fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS %s ON ONLY %s (%s)",
		pq.QuoteIdentifier(policy.indexName),
		pq.QuoteIdentifier(policy.partitionedTable),
		policy.partitionedIndexExpr,
	)
	if _, err := db.ExecContext(ctx, parentDDL); err != nil {
		return fmt.Errorf("create partitioned parent index %s: %w", policy.indexName, err)
	}

	for _, partition := range partitions {
		childIndex := fmt.Sprintf("%s_p%d", policy.indexName, partition.oid)
		attached, err := partitionIndexIsAttached(ctx, db, policy.indexName, childIndex)
		if err != nil {
			return err
		}
		if attached {
			continue
		}
		attachDDL := fmt.Sprintf(
			"ALTER INDEX %s ATTACH PARTITION %s.%s",
			pq.QuoteIdentifier(policy.indexName),
			pq.QuoteIdentifier(partition.schema),
			pq.QuoteIdentifier(childIndex),
		)
		if _, err := db.ExecContext(ctx, attachDDL); err != nil {
			return fmt.Errorf("attach partition index %s: %w", childIndex, err)
		}
	}

	valid, err = qualifiedIndexIsValid(ctx, db, "public", policy.indexName)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("partitioned parent index %s remains invalid after attaching all known partitions", policy.indexName)
	}
	return nil
}

func listTablePartitions(ctx context.Context, db *sql.DB, table string) ([]tablePartition, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT child_ns.nspname, child.relname, child.oid
		FROM pg_inherits inheritance
		JOIN pg_class parent ON parent.oid = inheritance.inhparent
		JOIN pg_namespace parent_ns ON parent_ns.oid = parent.relnamespace
		JOIN pg_class child ON child.oid = inheritance.inhrelid
		JOIN pg_namespace child_ns ON child_ns.oid = child.relnamespace
		WHERE parent_ns.nspname = current_schema() AND parent.relname = $1
		ORDER BY child.oid`, table)
	if err != nil {
		return nil, fmt.Errorf("list partitions for %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var partitions []tablePartition
	for rows.Next() {
		var partition tablePartition
		if err := rows.Scan(&partition.schema, &partition.name, &partition.oid); err != nil {
			return nil, fmt.Errorf("scan partition for %s: %w", table, err)
		}
		partitions = append(partitions, partition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate partitions for %s: %w", table, err)
	}
	if len(partitions) == 0 {
		return nil, fmt.Errorf("partitioned table %s has no partitions", table)
	}
	return partitions, nil
}

func qualifiedIndexIsInvalid(ctx context.Context, db *sql.DB, schema, index string) (bool, error) {
	valid, exists, err := qualifiedIndexState(ctx, db, schema, index)
	return exists && !valid, err
}

func qualifiedIndexIsValid(ctx context.Context, db *sql.DB, schema, index string) (bool, error) {
	valid, exists, err := qualifiedIndexState(ctx, db, schema, index)
	return exists && valid, err
}

func qualifiedIndexState(ctx context.Context, db *sql.DB, schema, index string) (valid, exists bool, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT i.indisvalid
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`, schema, index).Scan(&valid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("check index %s.%s: %w", schema, index, err)
	}
	return valid, true, nil
}

func partitionIndexIsAttached(ctx context.Context, db *sql.DB, parentIndex, childIndex string) (bool, error) {
	var attached bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_inherits inheritance
			JOIN pg_class parent ON parent.oid = inheritance.inhparent
			JOIN pg_class child ON child.oid = inheritance.inhrelid
			WHERE parent.relname = $1 AND child.relname = $2
		)`, parentIndex, childIndex).Scan(&attached)
	if err != nil {
		return false, fmt.Errorf("check partition index attachment %s -> %s: %w", childIndex, parentIndex, err)
	}
	return attached, nil
}
