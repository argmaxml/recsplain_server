package main

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"strconv"
	"testing"
	"time"

	"github.com/go-redis/redis/v9"
)

func TestGet(t *testing.T) {
	// read json file
	credentials_json, err := ioutil.ReadFile("credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	credentials := make(map[string]string)
	err = json.Unmarshal(credentials_json, &credentials)

	var ctx = context.Background()
	redis_db, _ := strconv.Atoi(credentials["redis_db"])
	rdb := redis.NewClient(&redis.Options{
		Addr:     credentials["redis_addr"],
		Password: credentials["redis_password"],
		DB:       redis_db,
	})
	expected := "Hello World"
	rdb.Set(ctx, "TEST", expected, 10*time.Second)
	actual := rdb.Get(ctx, "TEST").Val()
	if actual != expected {
		t.Errorf("Expected %s, got %s", expected, actual)
	}

}
func TestList(t *testing.T) {
	// read json file
	credentials_json, err := ioutil.ReadFile("credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	credentials := make(map[string]string)
	err = json.Unmarshal(credentials_json, &credentials)

	var ctx = context.Background()
	redis_db, _ := strconv.Atoi(credentials["redis_db"])
	rdb := redis.NewClient(&redis.Options{
		Addr:     credentials["redis_addr"],
		Password: credentials["redis_password"],
		DB:       redis_db,
	})
	// rdb.RPush(ctx, "LIST", "1", 10*time.Second)
	// rdb.RPush(ctx, "LIST", "2", 10*time.Second)
	rdb.Set(ctx, "LIST", "o", 10*time.Second)
	rdb.Append(ctx, "LIST", "a")
	rdb.Append(ctx, "LIST", "b")
	rdb.Append(ctx, "LIST", "c")
	actual := rdb.GetRange(ctx, "LIST", 0, 2).Val()
	expected := "oab"
	if actual != expected {
		t.Errorf("Expected %s, got %s", expected, actual)
	}

}
