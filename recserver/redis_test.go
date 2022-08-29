package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-redis/redis/v9"
)

func TestKeys(t *testing.T) {
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
	expected := "USER_1,USER_2,USER_3,USER_4"
	rdb.Set(ctx, "USER_1", expected, 2*time.Second)
	rdb.Set(ctx, "USER_2", expected, 2*time.Second)
	rdb.Set(ctx, "USER_3", expected, 2*time.Second)
	rdb.Set(ctx, "USER_4", expected, 2*time.Second)
	keys := rdb.Keys(ctx, "USER_*").Val()
	sort.Strings(keys)
	actual := strings.Join(keys, ",")
	if actual != expected {
		t.Errorf("Expected %s, got %s", expected, actual)
	}

}

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
	rdb.Del(ctx, "LIST")
	rdb.RPush(ctx, "LIST", "z z")
	rdb.LPush(ctx, "LIST", "a a")
	len := rdb.LLen(ctx, "LIST").Val()
	fmt.Fprintf(os.Stderr, "len: %d\n", len)
	actual := rdb.LRange(ctx, "LIST", 0, 2).Val()
	expected := "a a~z z"
	if strings.Join(actual, "~") != expected {
		t.Errorf("Expected %s, got %s", expected, actual)
	}

}
