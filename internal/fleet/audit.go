package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxAuditSize = 16 << 20

type AuditRecord struct {
	OperationID  string    `json:"operationId"`
	ClusterID    string    `json:"clusterId"`
	ConnectionID string    `json:"connectionId"`
	Action       string    `json:"action"`
	Resource     string    `json:"resource"`
	Namespace    string    `json:"namespace"`
	Name         string    `json:"name"`
	Stage        string    `json:"stage"`
	Outcome      string    `json:"outcome,omitempty"`
	ObservedAt   time.Time `json:"observedAt"`
	Actor        string    `json:"actor"`
}

type AuditStore struct {
	mu   sync.Mutex
	path string
}

func newAuditStore(path string) *AuditStore {
	return &AuditStore{path: path}
}

func (s *AuditStore) Append(record AuditRecord) error {
	if s == nil {
		return errors.New("fleet audit is unavailable")
	}
	if err := validateAuditRecord(record); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _, exists, err := s.readLocked()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return errors.New("fleet audit record is invalid")
	}
	encoded = append(encoded, '\n')
	if int64(len(data)+len(encoded)) > maxAuditSize {
		return errors.New("fleet audit is full")
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return errors.New("fleet audit is unavailable")
	}
	info, statErr := file.Stat()
	pathInfo, pathErr := os.Lstat(s.path)
	if statErr != nil || pathErr != nil || !info.Mode().IsRegular() ||
		info.Mode().Perm() != 0600 || pathInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(info, pathInfo) {
		_ = file.Close()
		return errors.New("fleet audit file is insecure")
	}
	n, writeErr := file.Write(encoded)
	if writeErr == nil && n != len(encoded) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("fleet audit could not be persisted")
	}
	if !exists {
		if dir, err := os.Open(filepath.Dir(s.path)); err == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
	}
	return nil
}

func (s *AuditStore) Read(limit int) ([]AuditRecord, bool, error) {
	if s == nil {
		return []AuditRecord{}, false, errors.New("fleet audit is unavailable")
	}
	if limit <= 0 || limit > 100 {
		return []AuditRecord{}, false, errors.New("fleet audit limit is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, records, _, err := s.readLocked()
	if err != nil {
		return []AuditRecord{}, false, err
	}
	truncated := len(records) > limit
	if truncated {
		records = records[len(records)-limit:]
	}
	out := make([]AuditRecord, len(records))
	copy(out, records)
	return out, truncated, nil
}

func (s *AuditStore) readLocked() ([]byte, []AuditRecord, bool, error) {
	info, err := os.Lstat(s.path)
	if os.IsNotExist(err) {
		return nil, []AuditRecord{}, false, nil
	}
	if err != nil {
		return nil, nil, false, errors.New("fleet audit is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxAuditSize {
		return nil, nil, false, errors.New("fleet audit file is invalid")
	}
	file, err := os.Open(s.path)
	if err != nil {
		return nil, nil, false, errors.New("fleet audit is unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 || !os.SameFile(info, opened) {
		return nil, nil, false, errors.New("fleet audit file is insecure")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxAuditSize+1))
	if err != nil || len(data) > maxAuditSize {
		return nil, nil, false, errors.New("fleet audit file is invalid")
	}
	if len(data) == 0 {
		return data, []AuditRecord{}, true, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, nil, false, errors.New("fleet audit history is incomplete")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	records := make([]AuditRecord, 0, len(lines))
	for _, line := range lines {
		var record AuditRecord
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil || decoder.Decode(new(any)) != io.EOF || validateAuditRecord(record) != nil {
			return nil, nil, false, errors.New("fleet audit history is invalid")
		}
		records = append(records, record)
	}
	return data, records, true, nil
}

func validateAuditRecord(record AuditRecord) error {
	if record.OperationID == "" || record.ClusterID == "" || record.ConnectionID == "" ||
		record.Action == "" || record.Resource == "" || record.Namespace == "" ||
		record.Name == "" || record.Actor == "" || record.ObservedAt.IsZero() ||
		(record.Stage != "intent" && record.Stage != "result") ||
		(record.Stage == "intent" && record.Outcome != "") ||
		(record.Stage == "result" && record.Outcome == "") {
		return errors.New("fleet audit record is invalid")
	}
	return nil
}
