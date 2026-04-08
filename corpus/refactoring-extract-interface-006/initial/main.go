package main

import (
	"errors"
	"fmt"
	"sync"
)

// FileStore simulates a file-backed key-value store.
type FileStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

// NewFileStore creates a new FileStore.
func NewFileStore() *FileStore {
	return &FileStore{data: make(map[string][]byte)}
}

// Save writes data to the file store.
func (f *FileStore) Save(key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key == "" {
		return errors.New("empty key")
	}
	f.data[key] = append([]byte(nil), data...)
	return nil
}

// Load reads data from the file store.
func (f *FileStore) Load(key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	val, ok := f.data[key]
	if !ok {
		return nil, fmt.Errorf("key %q not found", key)
	}
	return append([]byte(nil), val...), nil
}

// DBStore simulates a database-backed key-value store.
type DBStore struct {
	mu      sync.Mutex
	records map[string][]byte
}

// NewDBStore creates a new DBStore.
func NewDBStore() *DBStore {
	return &DBStore{records: make(map[string][]byte)}
}

// Save writes data to the database store.
func (d *DBStore) Save(key string, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if key == "" {
		return errors.New("empty key")
	}
	d.records[key] = append([]byte(nil), data...)
	return nil
}

// Load reads data from the database store.
func (d *DBStore) Load(key string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	val, ok := d.records[key]
	if !ok {
		return nil, fmt.Errorf("key %q not found in db", key)
	}
	return append([]byte(nil), val...), nil
}

func main() {
	fs := NewFileStore()
	ds := NewDBStore()

	fs.Save("greeting", []byte("hello from file"))
	ds.Save("greeting", []byte("hello from db"))

	data, _ := fs.Load("greeting")
	fmt.Println("FileStore:", string(data))

	data, _ = ds.Load("greeting")
	fmt.Println("DBStore:", string(data))
}
