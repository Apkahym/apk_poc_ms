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

	authDatabase := getAuthDatabaseName()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	collection := a.client.Database(authDatabase).Collection("api_keys")
	log.Printf("auth_lookup database=%s collection=%s route_key=%s", authDatabase, collection.Name(), key)

	var document bson.M
	if err := collection.FindOne(ctx, bson.M{"key": key}).Decode(&document); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			log.Printf("auth_lookup_missing database=%s collection=%s route_key=%s attempting_create=true", authDatabase, collection.Name(), key)
			if insertErr := a.ensureMissingAuthKey(ctx, collection, key); insertErr != nil {
				log.Printf("auth_lookup_create_failed database=%s collection=%s route_key=%s error=%v", authDatabase, collection.Name(), key, insertErr)
				return "", insertErr
			}
			return "key-not-found", nil
		}
		log.Printf("auth_lookup_error database=%s collection=%s route_key=%s error=%v", authDatabase, collection.Name(), key, err)
		return "", err
	}

	value, _ := document["value"].(string)
	if value == "" || value == "key-not-found" {
		log.Printf("auth_key_unauthorized database=%s collection=%s route_key=%s value=%q", authDatabase, collection.Name(), key, value)
		return "key-not-found", nil
	}
	return value, nil
}

func (a *APIKeyAuth) ensureMissingAuthKey(ctx context.Context, collection *mongo.Collection, key string) error {
	if _, err := collection.InsertOne(ctx, bson.M{
		"key":   key,
		"value": "key-not-found",
	}); err != nil {
		log.Printf("auth_seed_insert_failed database=%s collection=%s key=%s error=%v", collection.Database().Name(), collection.Name(), key, err)
		return err
	}
	log.Printf("auth_seed_inserted database=%s collection=%s key=%s value=key-not-found", collection.Database().Name(), collection.Name(), key)
	return nil
}

func (a *APIKeyAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL == nil {
			log.Printf("auth_invalid_request no_url request_uri=%q", r.RequestURI)
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "USR401")
			return
		}

		routeKey, routeErr := routeKey(r.URL.Path)
		if routeErr != nil {
			log.Printf("auth_invalid_route path=%q error=%v", r.URL.Path, routeErr)
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "USR401")
			return
		}

		apiKey := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if apiKey == "" {
			if _, getErr := a.Get(routeKey); getErr != nil {
				log.Printf("auth_missing_header route_key=%s database=%s error=%v", routeKey, getAuthDatabaseName(), getErr)
			}
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "USR401")
			return
		}

		storedKey, err := a.Get(routeKey)
		if err != nil {
			log.Printf("auth_lookup_failed route_key=%s database=%s api_key_present=%t error=%v", routeKey, getAuthDatabaseName(), true, err)
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "USR401")
			return
		}
		if storedKey == "key-not-found" || storedKey != apiKey {
			log.Printf("auth_denied route_key=%s database=%s collection=%s request_key_present=%t stored_key_present=%t", routeKey, getAuthDatabaseName(), "api_keys", apiKey != "", storedKey != "")
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

func getAuthDatabaseName() string {
	databaseName := strings.TrimSpace(os.Getenv("AUTH_DATABASE"))
	if databaseName == "" {
		return defaultAuthDB
	}
	return databaseName
}

func writeAuthError(w http.ResponseWriter, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message, "code": code})
	log.Printf("auth_error code=%s message=%s", code, message)
}
