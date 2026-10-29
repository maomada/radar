package fleet

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditStoreDurableAndRejectsTornHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.audit.jsonl")
	store := newAuditStore(path)
	record := AuditRecord{
		OperationID: "op", ClusterID: "east", ConnectionID: "conn", Action: "scale",
		Resource: "Deployment", Namespace: "prod", Name: "web", Stage: "intent",
		Actor: "user:alice", ObservedAt: time.Now().UTC(),
	}
	if err := store.Append(record); err != nil {
		t.Fatal(err)
	}
	reopened := newAuditStore(path)
	records, truncated, err := reopened.Read(100)
	if err != nil || truncated || len(records) != 1 || !records[0].ObservedAt.Equal(record.ObservedAt) {
		t.Fatalf("audit read = %+v truncated=%v err=%v", records, truncated, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("audit mode = %v err=%v", info.Mode(), err)
	}
	if err := os.WriteFile(path, []byte(`{"operationId":"op"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.Read(100); err == nil {
		t.Fatal("torn audit accepted")
	}
}
