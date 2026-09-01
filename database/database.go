package database

import "go.mongodb.org/mongo-driver/mongo"

var client *mongo.Client

func SetClient(value *mongo.Client) {
	client = value
}

func GetClient() *mongo.Client {
	return client
}
