package routines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	mongoDB "apk_poc_ms/database"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type HTTPServer struct {
	addr   string
	server *http.Server
}

func NewHTTPServer() *HTTPServer {
	addr := strings.TrimSpace(os.Getenv("PORT"))
	if addr == "" {
		addr = "8080"
	}
	if !strings.HasPrefix(addr, ":") {
		addr = ":" + addr
	}
	return &HTTPServer{addr: addr}
}

func (s *HTTPServer) Start() error {
	if s.server != nil {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/", s.handleAPI)

	s.server = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       15 * time.Second,
	}

	go func() {
		if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server error: %v", err)
		}
	}()

	return nil
}

func (s *HTTPServer) Stop() error {
	if s.server == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.server.Shutdown(ctx); err != nil {
		return err
	}

	s.server = nil
	return nil
}

func (s *HTTPServer) Restart() error {
	if err := s.Stop(); err != nil {
		return err
	}
	return s.Start()
}

func (s *HTTPServer) Reload() error {
	return s.Restart()
}

func (s *HTTPServer) Message(body any) error {
	log.Printf("http_server message: %#v", body)
	return nil
}

func (s *HTTPServer) Process(handler func(), body any) error {
	if handler != nil {
		handler()
	}
	_ = body
	return nil
}

func (s *HTTPServer) handleAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	databaseName, collectionName, documentID, err := parseDatabaseCollectionPath(r.URL.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid route"})
		return
	}
	if documentID == "" {
		documentID = strings.TrimSpace(r.URL.Query().Get("id"))
	}

	if mongoDB.GetClient() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "mongo client not initialized"})
		return
	}

	collection := mongoDB.GetClient().Database(databaseName).Collection(collectionName)

	switch r.Method {
	case http.MethodGet:
		s.handleGet(w, r, collection, documentID)
	case http.MethodPost:
		s.handlePost(w, r, collection)
	case http.MethodPut:
		s.handlePut(w, r, collection, documentID)
	case http.MethodPatch:
		s.handlePatch(w, r, collection, documentID)
	case http.MethodDelete:
		s.handleDelete(w, r, collection, documentID)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *HTTPServer) handleGet(w http.ResponseWriter, r *http.Request, collection *mongo.Collection, documentID string) {
	if documentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "document id is required"})
		return
	}

	objectID, err := primitive.ObjectIDFromHex(documentID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid mongo ObjectID"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var doc bson.M
	if err := collection.FindOne(ctx, bson.M{"_id": objectID}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "document not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, sanitizeDocument(doc))
}

func (s *HTTPServer) handlePost(w http.ResponseWriter, r *http.Request, collection *mongo.Collection) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var filter map[string]any
	if err := decodeBody(r, &filter); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if len(filter) == 0 {
		filter = map[string]any{}
	}

	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	defer cursor.Close(ctx)

	var docs []bson.M
	if err := cursor.All(ctx, &docs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	payload := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		payload = append(payload, sanitizeDocument(doc))
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": payload, "count": len(payload)})
}

func (s *HTTPServer) handlePut(w http.ResponseWriter, r *http.Request, collection *mongo.Collection, documentID string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var document map[string]any
	if err := decodeBody(r, &document); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if document == nil {
		document = map[string]any{}
	}

	if documentID != "" {
		if _, ok := document["_id"]; !ok {
			document["_id"] = documentID
		}
	}
	if _, found := document["_id"]; !found {
		if rawID, ok := document["id"]; ok {
			document["_id"] = rawID
		} else {
			document["_id"] = primitive.NewObjectID()
		}
	}

	if objectID, ok := document["_id"].(string); ok {
		if oid, err := primitive.ObjectIDFromHex(objectID); err == nil {
			document["_id"] = oid
		}
	}

	result, err := collection.InsertOne(ctx, document)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	if objectID, ok := result.InsertedID.(primitive.ObjectID); ok {
		document["id"] = objectID.Hex()
		delete(document, "_id")
	} else if insertedID, ok := result.InsertedID.(string); ok {
		document["id"] = insertedID
		delete(document, "_id")
	}

	writeJSON(w, http.StatusCreated, sanitizeDocument(document))
}

func (s *HTTPServer) handlePatch(w http.ResponseWriter, r *http.Request, collection *mongo.Collection, documentID string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var payload map[string]any
	if err := decodeBody(r, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if len(payload) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body is required"})
		return
	}

	filter := bson.M{}
	if documentID != "" {
		objectID, err := primitive.ObjectIDFromHex(documentID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid mongo ObjectID"})
			return
		}
		filter["_id"] = objectID
	} else if rawFilter, ok := payload["filter"]; ok {
		filter = toBSONMap(rawFilter)
		delete(payload, "filter")
	}

	if len(filter) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "filter or document id is required"})
		return
	}

	updateDoc := payload
	if rawUpdate, ok := payload["update"]; ok {
		updateDoc = toBSONMap(rawUpdate)
		delete(payload, "update")
	}
	if !hasMongoOperator(updateDoc) {
		updateDoc = bson.M{"$set": updateDoc}
	}

	result, err := collection.UpdateOne(ctx, filter, updateDoc)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	if result.MatchedCount == 0 && result.UpsertedCount == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "document not found"})
		return
	}

	var doc bson.M
	if err := collection.FindOne(ctx, filter).Decode(&doc); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, sanitizeDocument(doc))
}

func (s *HTTPServer) handleDelete(w http.ResponseWriter, r *http.Request, collection *mongo.Collection, documentID string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	filter := bson.M{}
	if documentID != "" {
		objectID, err := primitive.ObjectIDFromHex(documentID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid mongo ObjectID"})
			return
		}
		filter["_id"] = objectID
	} else {
		var payload map[string]any
		if err := decodeBody(r, &payload); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if len(payload) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "document id or filter is required"})
			return
		}
		filter = toBSONMap(payload)
	}

	result, err := collection.DeleteOne(ctx, filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"deleted": result.DeletedCount > 0, "id": documentID})
}

func parseDatabaseCollectionPath(rawPath string) (string, string, string, error) {
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) != 4 && len(parts) != 5 {
		return "", "", "", fmt.Errorf("invalid route")
	}
	if parts[0] != "api" || parts[1] != "v1" {
		return "", "", "", fmt.Errorf("invalid route")
	}

	databaseName := parts[2]
	collectionName := parts[3]
	documentID := ""
	if len(parts) > 4 {
		documentID = parts[4]
	}

	if databaseName == "" || collectionName == "" {
		return "", "", "", fmt.Errorf("database and collection are required")
	}
	if !isValidMongoIdentifier(databaseName) || !isValidMongoIdentifier(collectionName) {
		return "", "", "", fmt.Errorf("database and collection names contain invalid characters")
	}

	return databaseName, collectionName, documentID, nil
}

func isValidMongoIdentifier(value string) bool {
	if value == "" {
		return false
	}

	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func decodeBody(r *http.Request, target *map[string]any) error {
	if r.Body == nil {
		return io.EOF
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return io.EOF
		}
		return err
	}
	return nil
}

func sanitizeDocument(input any) map[string]any {
	output := make(map[string]any)

	switch value := input.(type) {
	case bson.M:
		for key, v := range value {
			output[key] = v
		}
	case map[string]any:
		for key, v := range value {
			output[key] = v
		}
	default:
		encoded, err := bson.Marshal(value)
		if err == nil {
			_ = bson.Unmarshal(encoded, &output)
		}
	}

	if _, ok := output["_id"]; ok {
		switch oid := output["_id"].(type) {
		case primitive.ObjectID:
			output["id"] = oid.Hex()
		case string:
			output["id"] = oid
		default:
			output["id"] = fmt.Sprintf("%v", output["_id"])
		}
		delete(output, "_id")
	}

	return output
}

func toBSONMap(value any) bson.M {
	if filter, ok := value.(bson.M); ok {
		return filter
	}
	if filter, ok := value.(map[string]any); ok {
		return bson.M(filter)
	}
	result := bson.M{}
	encoded, err := bson.Marshal(value)
	if err == nil {
		_ = bson.Unmarshal(encoded, &result)
	}
	return result
}

func hasMongoOperator(value map[string]any) bool {
	for key := range value {
		if strings.HasPrefix(key, "$") {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("write response: %v", err)
	}
}
