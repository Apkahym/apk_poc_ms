package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

const defaultAuthDB = "auth"

// APIKeyAuth checks the X-API-Key header against the value stored in MongoDB.
type APIKeyAuth struct {
	client *mongo.Client
}

func NewAPIKeyAuth(client *mongo.Client) *APIKeyAuth {
	return &APIKeyAuth{client: client}
}

func (a *APIKeyAuth) Get(route string) (string, error) {
	if a == nil || a.client == nil {
		return "", errors.New("database client unavailable")
	}

	key, err := routeKey(route)
	if err != nil {
		return "", err
	}

	authDatabase := strings.TrimSpace(os.Getenv("AUTH_DATABASE"))
	if authDatabase == "" {
		authDatabase = defaultAuthDB
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := a.client.Database(authDatabase).Collection("api_keys")
	var document bson.M
	if err := collection.FindOne(ctx, bson.M{"key": key}).Decode(&document); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			if insertErr := a.ensureMissingAuthKey(ctx, collection, key); insertErr != nil {
				return "", insertErr
			}
			return "key-not-found", nil
		}
		return "", err
	}

	value, _ := document["value"].(string)
	if value == "" || value == "key-not-found" {
		return "key-not-found", nil
	}
	return value, nil
}

func (a *APIKeyAuth) ensureMissingAuthKey(ctx context.Context, collection *mongo.Collection, key string) error {
	if _, err := collection.InsertOne(ctx, bson.M{
		"key":   key,
		"value": "key-not-found",
	}); err != nil {
		return err
	}
	return nil
}

func (a *APIKeyAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL == nil {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "USR401")
			return
		}

		apiKey := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if apiKey == "" {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "USR401")
			return
		}

		routeKey, err := routeKey(r.URL.Path)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "USR401")
			return
		}

		storedKey, err := a.Get(routeKey)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "USR401")
			return
		}
		if storedKey == "key-not-found" || storedKey != apiKey {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "USR401")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func routeKey(path string) (string, error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "v1" {
		return "", fmt.Errorf("invalid route")
	}
	if strings.TrimSpace(parts[2]) == "" || strings.TrimSpace(parts[3]) == "" {
		return "", fmt.Errorf("invalid route")
	}
	return strings.TrimSpace(parts[2]) + "." + strings.TrimSpace(parts[3]), nil
}

func writeAuthError(w http.ResponseWriter, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message, "code": code})
	log.Printf("auth_error code=%s message=%s", code, message)
}
