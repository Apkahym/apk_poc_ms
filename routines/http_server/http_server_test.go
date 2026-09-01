package http_server

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestSanitizeDocumentConvertsObjectIDToID(t *testing.T) {
	input := bson.M{"_id": primitive.NewObjectID(), "name": "Alice"}
	result := sanitizeDocument(input)
	if _, ok := result["_id"]; ok {
		t.Fatal("_id should be removed from the output")
	}
	if _, ok := result["id"]; !ok {
		t.Fatal("id should be present in the output")
	}
	if result["name"] != "Alice" {
		t.Fatalf("expected name Alice, got %v", result["name"])
	}
}

func TestParseDatabaseCollectionPath(t *testing.T) {
	databaseName, collectionName, documentID, err := parseDatabaseCollectionPath("/api/v1/testdb/users/64bf123abc1234567890abcd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if databaseName != "testdb" || collectionName != "users" || documentID != "64bf123abc1234567890abcd" {
		t.Fatalf("unexpected parsed values: db=%s collection=%s id=%s", databaseName, collectionName, documentID)
	}
}

func TestPatchItemID(t *testing.T) {
	payload := map[string]any{"id": "64bf123abc1234567890abcd", "update": map[string]any{"name": "Alice"}}
	id := patchItemID(payload)
	if id != "64bf123abc1234567890abcd" {
		t.Fatalf("expected patch item id 64bf123abc1234567890abcd, got %#v", id)
	}
}

func TestProcessPatchBatchItemPreservesOriginalIDOnError(t *testing.T) {
	payload := map[string]any{"id": "64bf123abc1234567890abcd", "stock": 999}
	itemID := patchItemID(payload)
	if itemID != "64bf123abc1234567890abcd" {
		t.Fatalf("expected item id to be captured before mutation, got %#v", itemID)
	}
	delete(payload, "id")
	if patchItemID(payload) != nil {
		t.Fatal("payload should not keep id after mutation")
	}
	if itemID != "64bf123abc1234567890abcd" {
		t.Fatalf("captured item id was lost unexpectedly: %#v", itemID)
	}
}
