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

// GetCollection returns a handle to a MongoDB collection.
//
// NOTE: this calls log.Fatal when the client is nil, i.e. it takes the process
// down. That is defensible for the request paths that cannot do anything
// useful without a database, but it makes GetCollection unsafe to call from
// anything best-effort — check MongoReady first there. See RecordTryOnFailure.
func GetCollection(databaseName, collectionName string) *mongo.Collection {
	if Client == nil {
		log.Fatal("MongoDB client is not initialized")
	}
	return Client.Database(databaseName).Collection(collectionName)
}

// MongoReady reports whether a collection handle can be taken without killing
// the process.
//
// It exists because best-effort writes — diagnostics, post-mortems, anything
// whose failure must not change what the user is told — cannot be allowed to
// call GetCollection blind. Recording *why* a try-on failed must never be the
// reason the server stops answering.
func MongoReady() bool { return Client != nil }
