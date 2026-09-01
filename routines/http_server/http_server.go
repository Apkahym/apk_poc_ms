package http_server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"apk_poc_ms/database"
	"apk_poc_ms/routines"
	"apk_poc_ms/routines/http_server/middleware"

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

	auth := middleware.NewAPIKeyAuth(database.GetClient())
	handler := middleware.Apply(mux, auth.Middleware)

	s.server = &http.Server{
		Addr:              s.addr,
		Handler:           handler,
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
		writeAPIError(w, http.StatusBadRequest, "invalid route", "USR400", nil)
		return
	}
	if documentID == "" {
		documentID = strings.TrimSpace(r.URL.Query().Get("id"))
	}
	if database.GetClient() == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "database unavailable", "USR503", nil)
		return
	}

	collection := database.GetClient().Database(databaseName).Collection(collectionName)

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
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "USR405", nil)
	}
}

func (s *HTTPServer) handleGet(w http.ResponseWriter, r *http.Request, collection *mongo.Collection, documentID string) {
	if documentID == "" {
		writeAPIError(w, http.StatusBadRequest, "record id is required", "USR400", nil)
		return
	}
	objectID, err := primitive.ObjectIDFromHex(documentID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid record id", "USR400", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var doc bson.M
	if err := collection.FindOne(ctx, bson.M{"_id": objectID}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			writeAPIError(w, http.StatusNotFound, "record not found", "USR404", err)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "fail to read record", "USR500", err)
		return
	}
	writeJSON(w, http.StatusOK, sanitizeDocument(doc))
}

func (s *HTTPServer) handlePost(w http.ResponseWriter, r *http.Request, collection *mongo.Collection) {
	payload, err := decodeRequestBody(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "fail to read body", "USR400", err)
		return
	}
	filter := bson.M{}
	if payload == nil {
		filter = bson.M{}
	} else if itemMap, ok := payload.(map[string]any); ok {
		filter = toBSONMap(itemMap)
	} else if batch, ok := payload.([]any); ok {
		if len(batch) == 0 {
			writeAPIError(w, http.StatusBadRequest, "filter is required", "USR400", nil)
			return
		}
		filter = toBSONMap(batch[0])
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "fail to query records", "USR500", err)
		return
	}
	defer cursor.Close(ctx)

	var docs []bson.M
	if err := cursor.All(ctx, &docs); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "fail to read records", "USR500", err)
		return
	}

	result := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		result = append(result, sanitizeDocument(doc))
	}
	totalCount, err := countDocuments(collection)
	if err != nil {
		log.Printf("count_documents_failed database=%s collection=%s error=%v", collection.Database().Name(), collection.Name(), err)
		totalCount = int64(len(result))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result, "count": totalCount})
}

func (s *HTTPServer) handlePut(w http.ResponseWriter, r *http.Request, collection *mongo.Collection, documentID string) {
	payload, err := decodeRequestBody(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "fail to read body", "USR400", err)
		return
	}
	if payload == nil {
		writeAPIError(w, http.StatusBadRequest, "document body is required", "USR400", nil)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if batch, ok := payload.([]any); ok {
		items := make([]any, 0, len(batch))
		for _, item := range batch {
			doc, ok := item.(map[string]any)
			if !ok {
				writeAPIError(w, http.StatusBadRequest, "batch items must be objects", "USR400", nil)
				return
			}
			if documentID != "" {
				if _, exists := doc["_id"]; !exists {
					doc["_id"] = documentID
				}
			}
			items = append(items, doc)
		}
		inserted, err := collection.InsertMany(ctx, items)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "fail to create records", "USR500", err)
			return
		}
		response := make([]map[string]any, 0, len(inserted.InsertedIDs))
		for _, id := range inserted.InsertedIDs {
			response = append(response, map[string]any{"id": normalizeInsertedID(id)})
		}
		totalCount, err := countDocuments(collection)
		if err != nil {
			log.Printf("count_documents_failed database=%s collection=%s error=%v", collection.Database().Name(), collection.Name(), err)
			totalCount = int64(len(response))
		}
		writeJSON(w, http.StatusCreated, map[string]any{"data": response, "count": totalCount})
		return
	}

	document, ok := payload.(map[string]any)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "document body must be an object", "USR400", nil)
		return
	}
	if documentID != "" {
		if _, exists := document["_id"]; !exists {
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
		writeAPIError(w, http.StatusInternalServerError, "fail to create record", "USR500", err)
		return
	}

	response := sanitizeDocument(document)
	response["id"] = normalizeInsertedID(result.InsertedID)
	delete(response, "_id")
	writeJSON(w, http.StatusCreated, response)
}

func (s *HTTPServer) handlePatch(w http.ResponseWriter, r *http.Request, collection *mongo.Collection, documentID string) {
	payload, err := decodeRequestBody(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "fail to read body", "USR400", err)
		return
	}
	if payload == nil {
		writeAPIError(w, http.StatusBadRequest, "patch body is required", "USR400", nil)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if batch, ok := payload.([]any); ok {
		results := make([]map[string]any, 0, len(batch))
		errorsList := make([]map[string]any, 0)
		for _, item := range batch {
			entry, ok := item.(map[string]any)
			if !ok {
				errorsList = append(errorsList, map[string]any{"id": nil, "error": "batch items must be objects"})
				continue
			}
			result, err := applyPatchOperation(ctx, collection, documentID, entry)
			if err != nil {
				errorsList = append(errorsList, map[string]any{"id": patchItemID(entry), "error": normalizePatchError(err)})
				continue
			}
			results = append(results, result)
		}
		response := map[string]any{"partial_success": len(results)}
		if len(errorsList) > 0 {
			response["errors"] = errorsList
		}
		if len(results) > 0 {
			response["data"] = results
		}
		if len(errorsList) > 0 {
			writeJSON(w, http.StatusOK, response)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	entry, ok := payload.(map[string]any)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "patch body must be an object", "USR400", nil)
		return
	}
	result, err := applyPatchOperation(ctx, collection, documentID, entry)
	if err != nil {
		writeAPIError(w, getStatusForPatchError(err), getClientMessageForPatch(err), getCodeForPatchError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *HTTPServer) handleDelete(w http.ResponseWriter, r *http.Request, collection *mongo.Collection, documentID string) {
	payload, err := decodeRequestBody(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "fail to read body", "USR400", err)
		return
	}
	if documentID != "" {
		documentID = strings.TrimSpace(documentID)
	}

	if payload == nil {
		if documentID == "" {
			writeAPIError(w, http.StatusBadRequest, "record id or filter is required", "USR400", nil)
			return
		}
		result, err := collection.DeleteOne(ctxWithTimeout(r), bson.M{"_id": normalizeObjectID(documentID)})
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "fail to delete record", "USR500", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": result.DeletedCount > 0, "id": documentID})
		return
	}

	if batch, ok := payload.([]any); ok {
		ids := make([]string, 0, len(batch))
		for _, item := range batch {
			switch value := item.(type) {
			case string:
				ids = append(ids, value)
			case map[string]any:
				id, hasID := extractID(value)
				if hasID {
					ids = append(ids, id)
				}
			default:
				writeAPIError(w, http.StatusBadRequest, "batch items must be ids or objects", "USR400", nil)
				return
			}
		}
		if len(ids) == 0 {
			writeAPIError(w, http.StatusBadRequest, "record ids are required", "USR400", nil)
			return
		}

		mongoIDs := make([]any, 0, len(ids))
		for _, id := range ids {
			mongoIDs = append(mongoIDs, normalizeObjectID(id))
		}
		result, err := collection.DeleteMany(ctxWithTimeout(r), bson.M{"_id": bson.M{"$in": mongoIDs}})
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "fail to delete records", "USR500", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": result.DeletedCount, "count": result.DeletedCount, "ids": ids})
		return
	}

	entry, ok := payload.(map[string]any)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "record id or filter is required", "USR400", nil)
		return
	}

	if id, hasID := extractID(entry); hasID {
		result, err := collection.DeleteOne(ctxWithTimeout(r), bson.M{"_id": normalizeObjectID(id)})
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "fail to delete record", "USR500", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": result.DeletedCount > 0, "id": id})
		return
	}
	if rawFilter, ok := entry["filter"]; ok {
		filter := toBSONMap(rawFilter)
		result, err := collection.DeleteMany(ctxWithTimeout(r), filter)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "fail to delete records", "USR500", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": result.DeletedCount, "count": result.DeletedCount})
		return
	}

	filter := toBSONMap(entry)
	result, err := collection.DeleteMany(ctxWithTimeout(r), filter)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "fail to delete records", "USR500", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": result.DeletedCount, "count": result.DeletedCount})
}

func ctxWithTimeout(r *http.Request) context.Context {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	_ = cancel
	return ctx
}

func applyPatchOperation(ctx context.Context, collection *mongo.Collection, documentID string, payload map[string]any) (map[string]any, error) {
	filter := bson.M{}
	if documentID != "" {
		objectID, err := primitive.ObjectIDFromHex(documentID)
		if err != nil {
			return nil, fmt.Errorf("invalid record id")
		}
		filter["_id"] = objectID
	} else if rawFilter, ok := payload["filter"]; ok {
		filter = toBSONMap(rawFilter)
		delete(payload, "filter")
	} else if id, ok := extractID(payload); ok {
		filter["_id"] = normalizeObjectID(id)
		delete(payload, "id")
		delete(payload, "_id")
	}

	if len(filter) == 0 {
		return nil, fmt.Errorf("filter or record id required")
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
		return nil, err
	}
	if result.MatchedCount == 0 {
		return nil, fmt.Errorf("record not found")
	}

	var doc bson.M
	if err := collection.FindOne(ctx, filter).Decode(&doc); err != nil {
		return nil, err
	}
	return sanitizeDocument(doc), nil
}

func patchItemID(payload map[string]any) any {
	if id, ok := extractID(payload); ok {
		return id
	}
	if rawFilter, ok := payload["filter"]; ok {
		if filterMap, ok := rawFilter.(map[string]any); ok {
			if id, ok := extractID(filterMap); ok {
				return id
			}
		}
	}
	return nil
}

func normalizePatchError(err error) string {
	if err == nil {
		return "request failed"
	}
	if strings.Contains(err.Error(), "record not found") {
		return "record not found"
	}
	if strings.Contains(err.Error(), "required") {
		return "request body is incomplete"
	}
	if strings.Contains(err.Error(), "invalid") {
		return "invalid record id"
	}
	return "fail to update record"
}

func getStatusForPatchError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if strings.Contains(err.Error(), "record not found") {
		return http.StatusNotFound
	}
	if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "invalid") {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func getClientMessageForPatch(err error) string {
	if err == nil {
		return "request processed"
	}
	if strings.Contains(err.Error(), "record not found") {
		return "record not found"
	}
	if strings.Contains(err.Error(), "required") {
		return "request body is incomplete"
	}
	if strings.Contains(err.Error(), "invalid") {
		return "invalid record id"
	}
	return "fail to update record"
}

func getCodeForPatchError(err error) string {
	if err == nil {
		return "USR200"
	}
	if strings.Contains(err.Error(), "record not found") {
		return "USR404"
	}
	if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "invalid") {
		return "USR400"
	}
	return "USR500"
}

func writeAPIError(w http.ResponseWriter, status int, message, code string, err error) {
	file, line := callerInfo()
	if err == nil {
		log.Printf("api_error message=%q code=%s file=%s:%d", message, code, file, line)
	} else {
		log.Printf("api_error message=%q code=%s file=%s:%d original=%v", message, code, file, line, err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message, "code": code})
}

func callerInfo() (string, int) {
	_, file, line, ok := runtime.Caller(2)
	if !ok {
		return "unknown", 0
	}
	return filepath.Base(file), line
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

func decodeRequestBody(r *http.Request) (any, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	var payload any
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&payload); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	return payload, nil
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

func countDocuments(collection *mongo.Collection) (int64, error) {
	if collection == nil {
		return 0, fmt.Errorf("collection is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return collection.CountDocuments(ctx, bson.M{})
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

func extractID(value map[string]any) (string, bool) {
	if value == nil {
		return "", false
	}
	for _, key := range []string{"id", "_id"} {
		if raw, ok := value[key]; ok {
			if id := normalizeInsertedID(raw); id != "" {
				return id, true
			}
		}
	}
	return "", false
}

func normalizeInsertedID(value any) string {
	switch id := value.(type) {
	case primitive.ObjectID:
		return id.Hex()
	case string:
		return id
	case []byte:
		return string(id)
	default:
		if id == nil {
			return ""
		}
		return fmt.Sprintf("%v", id)
	}
}

func normalizeObjectID(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return trimmed
	}
	if oid, err := primitive.ObjectIDFromHex(trimmed); err == nil {
		return oid
	}
	return trimmed
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("write response: %v", err)
	}
}

var _ routines.Routine = (*HTTPServer)(nil)
