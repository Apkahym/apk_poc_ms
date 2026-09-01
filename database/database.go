package database

import "go.mongodb.org/mongo-driver/mongo"

var Client *mongo.Client

func GetClient() *mongo.Client {
	return Client
}
