package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DataIntelligenceCrew/go-faiss"
	"github.com/bluele/gcache"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/template/html"
	"gonum.org/v1/gonum/mat"
)

func start_server(port int, schema Schema, variants []Variant, indices IndexCache, item_lookup ItemLookup, partitioned_records map[int][]Record, user_data map[string][]string) {
	app := fiber.New(fiber.Config{
		Views: html.New("./views", ".html"),
	})

	var popular_items map[int][]string
	popular_items = calc_popular_items(partitioned_records, user_data)

	var faiss_index faiss.Index
	app.Get("/npy/*", func(c *fiber.Ctx) error {
		m := read_npy(c.Params("*") + ".npy")
		msg := fmt.Sprintf("data = %v\n", mat.Formatted(m, mat.Prefix("       ")))
		return c.SendString(msg)
	})
	app.Get("/json/*", func(c *fiber.Ctx) error {
		filename := c.Params("*") + ".json"
		content, err := os.ReadFile(filename)
		if err != nil {
			return c.SendString("{\"Status\": \"Error\"}")
		}
		return c.SendString("{\"Status\": \"OK\", \"Data\": " + string(content) + "}")
	})

	app.Get("/partitions", func(c *fiber.Ctx) error {
		response := make(map[string](map[string]PartitionInfo))
		for _, variant := range variants {
			response[variant.Name] = make(map[string]PartitionInfo)
		}
		for partition_idx, partition := range partitioned_records {
			variant := schema.Partitions[partition_idx][0]
			key := strings.Join(schema.Partitions[partition_idx][1:], ",")
			info := PartitionInfo{
				Count: len(partition),
				Index: partition_idx,
			}
			response[variant][key] = info
		}
		return c.JSON(response)
	})

	app.Get("/labels", func(c *fiber.Ctx) error {
		return c.JSON(item_lookup.id2label)
	})

	app.Get("/version", func(c *fiber.Ctx) error {
		return c.SendString("202208282335")
	})

	app.Get("/reload_items", func(c *fiber.Ctx) error {
		partitioned_records, item_lookup, _ = schema.pull_item_data(variants)
		popular_items = calc_popular_items(partitioned_records, user_data)
		os.RemoveAll("indices")
		schema.index_partitions(partitioned_records)
		return c.SendString("{\"Status\": \"OK\"}")
	})

	app.Get("/reload_users", func(c *fiber.Ctx) error {
		var err error
		if user_data == nil {
			return c.SendString("User history not available in sources list")
		}
		user_data, err = schema.pull_user_data()
		if err != nil {
			return c.SendString(err.Error())
		}
		popular_items = calc_popular_items(partitioned_records, user_data)
		return c.SendString("{\"Status\": \"OK\"}")
	})

	app.Get("/user/*", func(c *fiber.Ctx) error {
		user_id := c.Params("*")
		user_history, found := user_data[user_id]
		if !found {
			return c.SendString("{\"Status\": \"User not found\"}")
		}
		response := struct {
			History []string `json:"history"`
			Variant string   `json:"variant"`
			Hash    int      `json:"hash"`
		}{
			History: user_history,
			Variant: pseudo_random_variant(user_id, variants),
			Hash:    hash_string(user_id, 1000),
		}
		return c.JSON(response)
	})

	app.Get("/items/*", func(c *fiber.Ctx) error {
		partition_number, err := strconv.Atoi(c.Params("*"))
		if err != nil || partition_number < 0 || partition_number >= len(schema.Partitions) {
			var found bool
			partition_number, found = schema.PartitionMap["~"+c.Params("*")]
			if !found {
				return c.SendString("{\"Status\": \"Partition not found\"}")
			}
		}
		response := make([]string, 0)
		for _, item_id := range partitioned_records[partition_number] {
			response = append(response, item_id.Label)
		}
		return c.JSON(response)
	})

	app.Get("/popular_items/*", func(c *fiber.Ctx) error {
		partition_number, err := strconv.Atoi(c.Params("*"))
		if err != nil || partition_number < 0 || partition_number >= len(schema.Partitions) {
			var found bool
			partition_number, found = schema.PartitionMap["~"+c.Params("*")]
			if !found {
				return c.SendString("{\"Status\": \"Partition not found\"}")
			}
		}
		return c.JSON(popular_items[partition_number])
	})

	app.Post("/encode", func(c *fiber.Ctx) error {
		var query map[string]string
		json.Unmarshal(c.Body(), &query)
		encoded := schema.encode(query)
		response := struct {
			Vector []float32 `json:"vector"`

			Partition int `json:"partition"`
		}{
			Vector:    encoded,
			Partition: schema.partition_number(query, ""),
		}
		return c.JSON(response)
	})

	app.Post("/item_query/:k?", func(c *fiber.Ctx) error {
		payload := struct {
			ItemId  string            `json:"id"`
			UserId  string            `json:"user_id"`
			Query   map[string]string `json:"query"`
			Explain bool              `json:"explain"`
			Variant string            `json:"variant"`
		}{}

		if err := c.BodyParser(&payload); err != nil {
			return c.JSON(fallbackResponse(popular_items, "Cannot parse payload", -1, 10))
		}
		k, err := strconv.Atoi(c.Params("k"))
		if err != nil {
			k = 2
		}
		var partition_idx int
		var encoded []float32
		var variant string
		if payload.Query["variant"] != "" {
			payload.Variant = payload.Query["variant"]
		}
		if payload.Variant == "random" {
			partition_idx = schema.partition_number(payload.Query, "")
			return c.JSON(randomResponse(partitioned_records, partition_idx, k))
		} else if payload.Variant == "popular" {
			partition_idx = schema.partition_number(payload.Query, "")
			return c.JSON(fallbackResponse(popular_items, "", partition_idx, k))
		} else if payload.Variant == "default" {
			variant = ""
		} else if payload.Variant == "" {
			variant = pseudo_random_variant(payload.UserId, variants)
		} else {
			variant = payload.Variant
		}
		if payload.ItemId != "" {
			id := int64(item_lookup.label2id[variant+"~"+payload.ItemId])
			var found bool
			partition_idx, found = item_lookup.label2partition[variant+"~"+payload.ItemId]
			if !found {
				return c.JSON(fallbackResponse(popular_items, "Item not found", -1, k))
			}
			encoded = schema.reconstruct(partitioned_records, id, partition_idx)
			if encoded == nil {
				return c.JSON(fallbackResponse(popular_items, "Item could not be reconstructed", partition_idx, k))
			}
		} else {
			partition_idx = schema.partition_number(payload.Query, variant)
			if partition_idx == -1 {
				return c.JSON(fallbackResponse(popular_items, "Item filters were not supplied, unknown partition.", -1, k))
			}
			encoded = schema.encode(payload.Query)
		}
		faiss_index, err = indices.faiss_index_from_cache(partition_idx)
		if err != nil {
			return c.JSON(fallbackResponse(popular_items, err.Error(), partition_idx, k))
		}
		distances, ids, err := faiss_index.Search(encoded, int64(k))
		if err != nil {
			return c.JSON(fallbackResponse(popular_items, err.Error(), partition_idx, k))
		}
		retval := explanationResponse(schema, distances, ids, payload.Explain, variant, partitioned_records, partition_idx, encoded, item_lookup)
		return c.JSON(retval)
	})

	app.Post("/user_query/:k?", func(c *fiber.Ctx) error {
		payload := struct {
			UserId  string            `json:"id"`
			History []string          `json:"history"`
			Filters map[string]string `json:"filters"`
			Explain bool              `json:"explain"`
			Variant string            `json:"variant"`
		}{}

		if err := c.BodyParser(&payload); err != nil {
			return c.JSON(fallbackResponse(popular_items, "Cannot parse payload", -1, 10))
		}
		k, err := strconv.Atoi(c.Params("k"))
		if err != nil {
			k = 2
		}
		var variant string
		if payload.Filters["variant"] != "" {
			payload.Variant = payload.Filters["variant"]
		}
		if payload.Variant == "random" {
			partition_idx := schema.partition_number(payload.Filters, "")
			return c.JSON(randomResponse(partitioned_records, partition_idx, k))
		} else if payload.Variant == "popular" {
			partition_idx := schema.partition_number(payload.Filters, "")
			return c.JSON(fallbackResponse(popular_items, "", partition_idx, k))
		} else if payload.Variant == "default" {
			variant = ""
		} else if payload.Variant == "" {
			variant = pseudo_random_variant(payload.UserId, variants)
		} else {
			variant = payload.Variant
		}
		partition_idx := schema.partition_number(payload.Filters, variant)
		if partition_idx == -1 {
			// serialize filters to json
			filters_json, err := json.Marshal(payload.Filters)
			if err != nil {
				return c.JSON(fallbackResponse(popular_items, "Cannot serialize filters", -1, k))
			}

			return c.JSON(fallbackResponse(popular_items, "User filters error, unknown partition.\n"+string(filters_json), -1, k))
		}
		item_vecs := make([][]float32, 1)
		item_vecs[0] = make([]float32, schema.Dim) // zero_vector

		if payload.UserId != "" {
			//Override user history from the id, if provided
			if user_data == nil {
				return c.JSON(fallbackResponse(popular_items, "User history not available in sources list", partition_idx, k))
			}
			payload.History = user_data[payload.UserId]
		}
		// If user had no history
		if len(payload.History) == 0 {
			partition_idx := schema.partition_number(payload.Filters, "")
			return c.JSON(fallbackResponse(popular_items, "User has no history", partition_idx, k))
		}
		for _, item_id := range payload.History {
			id := int64(item_lookup.label2id[variant+"~"+item_id])
			if id == -1 {
				continue
			}
			reconstructed := schema.reconstruct(partitioned_records, id, partition_idx)
			if reconstructed == nil {
				continue
			}
			item_vecs = append(item_vecs, reconstructed)
		}
		user_vec := make([]float32, schema.Dim)
		for _, item_vec := range item_vecs {
			for i := range user_vec {
				user_vec[i] += item_vec[i] / float32(len(item_vecs))
			}
		}

		faiss_index, err = indices.faiss_index_from_cache(partition_idx)
		if err != nil {
			return c.JSON(fallbackResponse(popular_items, err.Error(), partition_idx, k))
		}
		distances, ids, err := faiss_index.Search(user_vec, int64(k))
		if err != nil {
			return c.JSON(fallbackResponse(popular_items, err.Error(), partition_idx, k))
		}
		retval := explanationResponse(schema, distances, ids, payload.Explain, variant, partitioned_records, partition_idx, user_vec, item_lookup)
		return c.JSON(retval)
	})

	app.Get("/", func(c *fiber.Ctx) error {
		fields := make([]string, 0)
		filters := make([]string, 0)
		for _, e := range schema.Filters {
			fields = append(fields, e.Field)
			filters = append(filters, e.Field)
		}
		for _, e := range schema.Encoders {
			fields = append(fields, e.Field)
		}
		return c.Render("index", fiber.Map{
			"Headline": "Recsplain API",
			"Fields":   fields,
			"Filters":  filters,
		})
	})

	app.Get("/shutdown", func(c *fiber.Ctx) error {
		fmt.Println("Shutting down")
		os.RemoveAll("indices")
		app.Shutdown()
		return c.SendStatus(200)
	})

	log.Fatal(app.Listen(fmt.Sprintf(":%d", port)))
}

func explanationResponse(schema Schema, distances []float32, ids []int64, explain bool, variant string, partitioned_records map[int][]Record, partition_idx int, vec []float32, item_lookup ItemLookup) QueryRetVal {
	retrieved := make([]Explanation, 0)
	for i, id := range ids {
		if id == -1 {
			continue
		}
		next_result := Explanation{
			Label:    strings.SplitN(item_lookup.id2label[int(id)], "~", 2)[1],
			Distance: distances[i],
		}
		if (explain) && (partitioned_records != nil) {
			reconstructed := schema.reconstruct(partitioned_records, id, partition_idx)
			if reconstructed != nil {
				total_distance, breakdown := schema.componentwise_distance(vec, reconstructed)
				next_result.Distance = total_distance
				next_result.Breakdown = breakdown
			}
		}
		retrieved = append(retrieved, next_result)
	}
	if variant == "" {
		variant = "default"
	}
	return QueryRetVal{
		Explanations: retrieved,
		Variant:      variant,
		Partition:    partition_idx,
		Error:        "",
		Timestamp:    time.Now().Unix(),
	}
}

func randomResponse(partitioned_records map[int][]Record, partition_idx int, k int) QueryRetVal {
	var labels []string
	var random_items []string
	if partition_idx == -1 {
		// Choose a random partition
		partition_idx = rand.Intn(len(partitioned_records))
	}
	labels = make([]string, len(partitioned_records[partition_idx]))
	for i, record := range partitioned_records[partition_idx] {
		labels[i] = record.Label
	}
	rand.Seed(time.Now().UnixNano())
	if len(labels) > k {
		rand.Shuffle(len(labels), func(i, j int) {
			labels[i], labels[j] = labels[j], labels[i]
		})
		random_items = labels[:k]
	} else {
		random_items = labels
	}

	retrieved := make([]Explanation, 0)
	for _, item_id := range random_items {
		retrieved = append(retrieved, Explanation{
			Label:     item_id,
			Distance:  0,
			Breakdown: nil,
		})
		k--
		if k <= 0 {
			break
		}
	}
	return QueryRetVal{
		Explanations: retrieved,
		Variant:      "random",
		Error:        "",
		Partition:    partition_idx,
		Timestamp:    time.Now().Unix(),
	}
}

func fallbackResponse(popular_items map[int][]string, message string, partition_idx int, k int) QueryRetVal {
	retrieved := make([]Explanation, 0)
	for _, item_id := range popular_items[partition_idx] {
		retrieved = append(retrieved, Explanation{
			Label:     item_id,
			Distance:  0,
			Breakdown: nil,
		})
		k--
		if k <= 0 {
			break
		}
	}
	return QueryRetVal{
		Explanations: retrieved,
		Variant:      "popular",
		Error:        message,
		Partition:    partition_idx,
		Timestamp:    time.Now().Unix(),
	}
}

func calc_popular_items(partitioned_records map[int][]Record, user_data map[string][]string) map[int][]string {
	if user_data == nil {
		//No user data, so no popular items - return something
		ret := make(map[int][]string)
		ret[0] = make([]string, 0)
		for idx, record := range partitioned_records[0] {
			ret[0] = append(ret[0], record.Label)
			if idx > 100 {
				break
			}
		}
		//for partition error
		ret[-1] = ret[0]
		return ret
	}
	popular_items := make(map[int][]string)
	counter := make(map[string]int)
	for _, items := range user_data {
		for _, item := range items {
			counter[item]++
		}
	}
	//sort by count
	sorted_items := make([]string, 0, len(counter))
	for k := range counter {
		sorted_items = append(sorted_items, k)
	}
	sort.Strings(sorted_items)
	// reverse sorted_items
	for i, j := 0, len(sorted_items)-1; i < j; i, j = i+1, j-1 {
		sorted_items[i], sorted_items[j] = sorted_items[j], sorted_items[i]
	}
	popular_items[-1] = sorted_items[:100]

	for partition_idx, records := range partitioned_records {
		popular_items[partition_idx] = make([]string, 0)
		n := 100
		for _, item := range sorted_items {
			found_in_partition := false
			for _, record := range records {
				if record.Label == item {
					found_in_partition = true
					break
				}
			}
			if !found_in_partition {
				continue
			}
			popular_items[partition_idx] = append(popular_items[partition_idx], item)
			n--
			if n == 0 {
				break
			}
		}
		//Backfill with popular items from the partition
		for _, record := range records {
			if n <= 0 {
				break
			}
			popular_items[partition_idx] = append(popular_items[partition_idx], record.Label)
			n--
		}
	}
	return popular_items
}

func main() {
	var useCache bool
	var port int
	var schema_file string
	var variants_file string
	flag.BoolVar(&useCache, "cache", false, "use cache")
	flag.IntVar(&port, "port", 8088, "port to listen on")
	flag.StringVar(&schema_file, "schema", "schema.json", "Schema file name")
	flag.StringVar(&variants_file, "variants", "variants.json", "Variants file name")
	flag.Parse()

	schema, variants, err := read_schema(schema_file, variants_file)
	if err != nil {
		log.Fatal(err)
	}

	var cached_indices gcache.Cache
	var indices []faiss.Index
	var partitioned_records map[int][]Record
	var user_data map[string][]string

	item_lookup := ItemLookup{
		id2label:        make([]string, 0),
		label2id:        make(map[string]int),
		label2partition: make(map[string]int),
	}
	partitioned_records, item_lookup, err = schema.pull_item_data(variants)
	if err != nil {
		log.Fatal(err)
	}
	user_data, err = schema.pull_user_data()
	if err != nil {
		log.Println(err)
	}

	schema.index_partitions(partitioned_records)

	if useCache {
		cached_indices = gcache.New(32).
			LFU().
			LoaderFunc(func(key interface{}) (interface{}, error) {
				ind, err := faiss.ReadIndex(fmt.Sprintf("./indices/%d", key), 0)
				return *ind, err
			}).
			EvictedFunc(func(key, value interface{}) {
				value.(faiss.Index).Delete()
			}).
			Build()
	} else {
		indices = make([]faiss.Index, len(schema.Partitions))
		for i, _ := range schema.Partitions {
			indices[i], err = faiss.ReadIndex(fmt.Sprintf("./indices/%d", i), 0)
			if err != nil {
				fmt.Println(err)
				indices[i] = nil
			}
		}
	}

	// Poll for changes:
	for _, src := range schema.Sources {
		if src.RefreshRate > 0 {
			//This is probably a bad idea, but it works for now
			go poll_endpoint(fmt.Sprintf("http://localhost:%d/reload_"+src.Record, port), src.RefreshRate)
		}
	}
	index_dict := IndexCache{
		cache:    cached_indices,
		array:    indices,
		useCache: useCache,
	}
	start_server(port, schema, variants, index_dict, item_lookup, partitioned_records, user_data)
}
