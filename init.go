package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"apk_poc_ms/database"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	dsn := strings.TrimSpace(os.Getenv("MONGO_DSN"))
	if dsn == "" {
		log.Fatal("MONGO_DSN is required")
	}

	if !strings.HasPrefix(dsn, "mongodb://") && !strings.HasPrefix(dsn, "mongodb+srv://") {
		log.Fatalf("invalid MONGO_DSN: expected mongodb:// or mongodb+srv:// scheme")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(dsn))
	if err != nil {
		log.Fatalf("failed to connect to MongoDB: %v", err)
	}

	if err := client.Ping(ctx, nil); err != nil {
		log.Fatalf("failed to ping MongoDB: %v", err)
	}

	database.Client = client
}
