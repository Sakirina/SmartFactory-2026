package store

import (
	"errors"
	"sort"
)

type Revision struct {
	Kind    string
	ID      string
	Version int64
	Members map[string]int64
}

func (tx *Tx) CheckRevisions(revisions []Revision) error {
	kinds := map[string]bool{}
	for _, expected := range revisions {
		if expected.Members != nil {
			kinds[expected.Kind] = true
		}
	}
	ordered := make([]string, 0, len(kinds))
	for kind := range kinds {
		ordered = append(ordered, kind)
	}
	sort.Strings(ordered)
	for _, kind := range ordered {
		if _, err := tx.ReadCollection(kind); err != nil {
			return err
		}
	}
	for _, expected := range revisions {
		if expected.Members != nil {
			rows, err := tx.QueryContext(tx.Ctx, "SELECT id,version FROM documents WHERE kind=$1", expected.Kind)
			if err != nil {
				return err
			}
			count := 0
			matches := true
			for rows.Next() {
				var id string
				var version int64
				if err := rows.Scan(&id, &version); err != nil {
					rows.Close()
					return err
				}
				count++
				matches = matches && expected.Members[id] == version
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if !matches || count != len(expected.Members) {
				return ErrConflict
			}
			continue
		}
		doc, err := tx.Read(expected.Kind, expected.ID)
		if errors.Is(err, ErrNotFound) && expected.Version == 0 {
			continue
		}
		if errors.Is(err, ErrNotFound) || (err == nil && doc.Version != expected.Version) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
	}
	return nil
}
