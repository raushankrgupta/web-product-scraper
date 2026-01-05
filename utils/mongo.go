package utils

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var Client *mongo.Client

// ConnectMongo initializes the MongoDB connection
func ConnectMongo(uri string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientOptions := options.Client().ApplyURI(uri)
	client, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		return fmt.Errorf("failed to connect to mongodb: %w", err)
	}

	// Ping the database
	err = client.Ping(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to ping mongodb: %w", err)
	}

	Client = client
	log.Println("Connected to MongoDB!")
	return nil
}

// GetCollection returns a handle to a MongoDB collection
func GetCollection(databaseName, collectionName string) *mongo.Collection {
	if Client == nil {
		log.Fatal("MongoDB client is not initialized")
	}
	return Client.Database(databaseName).Collection(collectionName)
}
