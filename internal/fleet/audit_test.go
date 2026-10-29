package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
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

func TestAuditStoreFiltersBeforeLimitAndReportsUnmatchedIntent(t *testing.T) {
	store := newAuditStore(filepath.Join(t.TempDir(), "state.audit.jsonl"))
	record := AuditRecord{
		ClusterID: "east", ConnectionID: "conn", Action: "scale", Resource: "Deployment",
		Namespace: "prod", Name: "web", Actor: "user:alice", ObservedAt: time.Now().UTC(),
	}
	aliceIntent := record
	aliceIntent.OperationID, aliceIntent.Stage = "alice-op", "intent"
	if err := store.Append(aliceIntent); err != nil {
		t.Fatal(err)
	}
	aliceResult := aliceIntent
	aliceResult.Stage, aliceResult.Outcome = "result", "applied"
	if err := store.Append(aliceResult); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		other := record
		other.ClusterID = "west"
		other.Actor, other.Stage, other.Outcome = "user:bob", "result", "applied"
		other.OperationID = "bob-" + strconv.Itoa(i)
		if err := store.Append(other); err != nil {
			t.Fatal(err)
		}
	}
	records, truncated, unmatched, err := store.ReadFiltered("", "user:alice", 100)
	if err != nil || truncated || unmatched || len(records) != 2 || records[0].ClusterID != "east" {
		t.Fatalf("alice audit = %+v truncated=%v unmatched=%v err=%v", records, truncated, unmatched, err)
	}
	records, truncated, unmatched, err = store.ReadFiltered("east", "", 1)
	if err != nil || !truncated || unmatched || len(records) != 1 || records[0].Stage != "result" {
		t.Fatalf("admin cluster audit = %+v truncated=%v unmatched=%v err=%v", records, truncated, unmatched, err)
	}
	openIntent := record
	openIntent.OperationID, openIntent.Stage, openIntent.Actor = "unmatched", "intent", "user:alice"
	if err := store.Append(openIntent); err != nil {
		t.Fatal(err)
	}
	_, _, unmatched, err = store.ReadFiltered("east", "user:alice", 100)
	if err != nil || !unmatched {
		t.Fatalf("unmatched intent = %v err=%v", unmatched, err)
	}
}

func TestAuditStorePropagatesEveryDirectorySyncFailure(t *testing.T) {
	store := newAuditStore(filepath.Join(t.TempDir(), "state.audit.jsonl"))
	calls := 0
	store.syncDir = func(string) error {
		calls++
		return errors.New("sync failure")
	}
	record := AuditRecord{
		OperationID: "op", ClusterID: "east", ConnectionID: "conn", Action: "scale",
		Resource: "Deployment", Namespace: "prod", Name: "web", Stage: "intent",
		Actor: "user:alice", ObservedAt: time.Now().UTC(),
	}
	if err := store.Append(record); err == nil {
		t.Fatal("directory sync failure was ignored")
	}
	record.OperationID = "op-2"
	if err := store.Append(record); err == nil || calls != 2 {
		t.Fatalf("second directory sync failure = %v, calls=%d", err, calls)
	}
}
