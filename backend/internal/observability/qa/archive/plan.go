package archive

import (
	"bufio"
	"container/heap"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type SourceDeltaPlan struct {
	WindowStart          time.Time `json:"window_start"`
	WindowEnd            time.Time `json:"window_end"`
	SourceRecordCount    int64     `json:"source_record_count"`
	CommittedRecordCount int64     `json:"committed_record_count"`
	DeltaRecordCount     int64     `json:"delta_record_count"`
	CommittedOnlyCount   int64     `json:"committed_only_count"`
	DeletionAuthorized   bool      `json:"deletion_authorized"`
}

func PlanSourceDelta(ctx context.Context, conn *sql.Conn, window Window, commit VerifiedCommit) (_ SourceDeltaPlan, resultErr error) {
	window.Start = window.Start.UTC()
	window.End = window.End.UTC()
	if conn == nil || !window.End.Equal(window.Start.Add(time.Hour)) {
		return SourceDeltaPlan{}, fmt.Errorf("plan requires one UTC hour and a database connection")
	}
	rows, err := conn.QueryContext(ctx, `
SELECT created_at, request_id FROM qa_records
WHERE created_at >= $1 AND created_at < $2
ORDER BY created_at, request_id`, window.Start, window.End)
	if err != nil {
		return SourceDeltaPlan{}, fmt.Errorf("read source identities: %w", err)
	}
	defer func() { _ = rows.Close() }()

	committed, err := newCommittedIdentityIterator(commit.Segments)
	if err != nil {
		return SourceDeltaPlan{}, err
	}
	defer func() { _ = committed.Close() }()
	plan := SourceDeltaPlan{WindowStart: window.Start, WindowEnd: window.End, DeletionAuthorized: false}
	currentCommitted, haveCommitted, err := committed.Next()
	if err != nil {
		return SourceDeltaPlan{}, err
	}
	if haveCommitted {
		plan.CommittedRecordCount++
	}
	var previousSource RecordIdentity
	havePreviousSource := false
	for rows.Next() {
		var source RecordIdentity
		if err := rows.Scan(&source.CreatedAt, &source.RequestID); err != nil {
			return SourceDeltaPlan{}, err
		}
		source.CreatedAt = source.CreatedAt.UTC()
		if source.RequestID == "" || source.CreatedAt.Before(window.Start) || !source.CreatedAt.Before(window.End) {
			return SourceDeltaPlan{}, fmt.Errorf("invalid source record identity")
		}
		if havePreviousSource && compareIdentity(previousSource, source) >= 0 {
			return SourceDeltaPlan{}, fmt.Errorf("duplicate or unordered source record identity")
		}
		previousSource, havePreviousSource = source, true
		plan.SourceRecordCount++

		for haveCommitted && compareIdentity(currentCommitted, source) < 0 {
			plan.CommittedOnlyCount++
			currentCommitted, haveCommitted, err = committed.Next()
			if err != nil {
				return SourceDeltaPlan{}, err
			}
			if haveCommitted {
				plan.CommittedRecordCount++
			}
		}
		if haveCommitted && compareIdentity(currentCommitted, source) == 0 {
			currentCommitted, haveCommitted, err = committed.Next()
			if err != nil {
				return SourceDeltaPlan{}, err
			}
			if haveCommitted {
				plan.CommittedRecordCount++
			}
		} else {
			plan.DeltaRecordCount++
		}
	}
	if err := rows.Err(); err != nil {
		return SourceDeltaPlan{}, err
	}
	for haveCommitted {
		plan.CommittedOnlyCount++
		_, haveCommitted, err = committed.Next()
		if err != nil {
			return SourceDeltaPlan{}, err
		}
		if haveCommitted {
			plan.CommittedRecordCount++
		}
	}
	return plan, nil
}

type committedIdentityIterator struct {
	streams  []*identityStream
	queue    identityHeap
	previous RecordIdentity
	havePrev bool
}

func newCommittedIdentityIterator(segments []VerifiedSegment) (*committedIdentityIterator, error) {
	iterator := &committedIdentityIterator{streams: make([]*identityStream, 0, len(segments))}
	for index, segment := range segments {
		file, err := os.Open(segment.IdentityPath)
		if err != nil {
			_ = iterator.Close()
			return nil, fmt.Errorf("open committed identities: %w", err)
		}
		stream := &identityStream{index: index, file: file, scanner: bufio.NewScanner(file)}
		stream.scanner.Buffer(make([]byte, 16*1024), maxManifestBytes)
		iterator.streams = append(iterator.streams, stream)
		if stream.scanner.Scan() {
			if err := json.Unmarshal(stream.scanner.Bytes(), &stream.current); err != nil {
				_ = iterator.Close()
				return nil, err
			}
			heap.Push(&iterator.queue, stream)
		} else if err := stream.scanner.Err(); err != nil {
			_ = iterator.Close()
			return nil, err
		}
	}
	heap.Init(&iterator.queue)
	return iterator, nil
}

func (i *committedIdentityIterator) Next() (RecordIdentity, bool, error) {
	if i.queue.Len() == 0 {
		return RecordIdentity{}, false, nil
	}
	stream := mustIdentityStream(heap.Pop(&i.queue))
	current := stream.current
	if i.havePrev && compareIdentity(i.previous, current) == 0 {
		return RecordIdentity{}, false, fmt.Errorf("duplicate record identity across committed segments")
	}
	i.previous, i.havePrev = current, true
	if stream.scanner.Scan() {
		if err := json.Unmarshal(stream.scanner.Bytes(), &stream.current); err != nil {
			return RecordIdentity{}, false, err
		}
		heap.Push(&i.queue, stream)
	} else if err := stream.scanner.Err(); err != nil {
		return RecordIdentity{}, false, err
	}
	return current, true, nil
}

func (i *committedIdentityIterator) Close() error {
	var first error
	for _, stream := range i.streams {
		if err := stream.file.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
