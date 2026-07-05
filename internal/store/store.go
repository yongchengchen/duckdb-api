package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"

	"duckdb-api/internal/model"
)

// Store is a JSON-file-backed registry of table definitions.
type Store struct {
	path   string
	mu     sync.RWMutex
	tables map[string]model.Table
}

func New(path string) (*Store, error) {
	s := &Store{path: path, tables: map[string]model.Table{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []model.Table
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, t := range list {
		s.tables[t.Name] = t
	}
	return s, nil
}

func (s *Store) List() []model.Table {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]model.Table, 0, len(s.tables))
	for _, t := range s.tables {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

func (s *Store) Get(name string) (model.Table, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tables[name]
	return t, ok
}

func (s *Store) Upsert(t model.Table) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tables[t.Name] = t
	return s.persist()
}

func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tables, name)
	return s.persist()
}

// persist must be called with s.mu held.
func (s *Store) persist() error {
	list := make([]model.Table, 0, len(s.tables))
	for _, t := range s.tables {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
