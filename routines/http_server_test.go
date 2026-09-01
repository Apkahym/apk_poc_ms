package routines

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
