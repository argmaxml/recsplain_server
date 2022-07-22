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
	// GET /api/register
	app.Get("/npy/*", func(c *fiber.Ctx) error {
		m := read_npy(c.Params("*") + ".npy")
		msg := fmt.Sprintf("data = %v\n", mat.Formatted(m, mat.Prefix("       ")))
		return c.SendString(msg)
	})

	app.Get("/partitions", func(c *fiber.Ctx) error {
		ret := make([]PartitionMeta, len(schema.Partitions))
		for i, partition := range schema.Partitions {
			ret[i].Name = partition
			//TODO: fix
			// ret[i].Count = int(indices[i].Ntotal())
			// ret[i].Trained = indices[i].IsTrained()
		}
		return c.JSON(ret)
	})

	app.Get("/labels", func(c *fiber.Ctx) error {
		return c.JSON(item_lookup.id2label)
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

	app.Get("/variants", func(c *fiber.Ctx) error {
		var err error
		filename := "variants.json"
		content, err := os.ReadFile(filename)
		if err != nil {
			return c.SendString("{\"Status\": \"Error\"}")
		}
		return c.SendString("{\"Status\": \"OK\", \"Data\": " + string(content) + "}")
	})

	app.Post("/encode", func(c *fiber.Ctx) error {
		var query map[string]string
		json.Unmarshal(c.Body(), &query)
		encoded := schema.encode(query)
		return c.JSON(encoded)
	})

	app.Post("/item_query/:k?", func(c *fiber.Ctx) error {
		payload := struct {
			ItemId  string            `json:"id"`
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
		if payload.Variant == "random" {
			partition_idx = schema.partition_number(payload.Query, "")
			return c.JSON(randomResponse(partitioned_records, partition_idx, k))
		}
		if payload.Variant == "popular" {
			partition_idx = schema.partition_number(payload.Query, "")
			return c.JSON(fallbackResponse(popular_items, "", partition_idx, k))
		}
		if payload.Variant == "" {
			variant = random_variant(variants)
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
		if payload.Variant == "random" {
			partition_idx := schema.partition_number(payload.Filters, "")
			return c.JSON(randomResponse(partitioned_records, partition_idx, k))
		}
		if payload.Variant == "popular" {
			partition_idx := schema.partition_number(payload.Filters, "")
			return c.JSON(fallbackResponse(popular_items, "", partition_idx, k))
		}
		if payload.Variant == "" {
			variant = random_variant(variants)
		} else {
			variant = payload.Variant
		}
		partition_idx := schema.partition_number(payload.Filters, variant)
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
		Error:        "",
		Timestamp:    time.Now().Unix(),
	}
}

func randomResponse(partitioned_records map[int][]Record, partition_idx int, k int) QueryRetVal {
	labels := make([]string, len(partitioned_records))
	for i, record := range partitioned_records[partition_idx] {
		labels[i] = record.Label
	}
	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(labels), func(i, j int) {
		labels[i], labels[j] = labels[j], labels[i]
	})
	random_items := labels[:k]

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

	}
	return popular_items
}

func main() {
	var useCache bool = true
	var port int
	var base_dir string
	flag.BoolVar(&useCache, "cache", true, "use cache")
	flag.IntVar(&port, "port", 8008, "port to listen on")
	flag.StringVar(&base_dir, "dir", ".", "Base directory for data")
	flag.Parse()

	schema, variants, err := read_schema(base_dir+"/schema.json", base_dir+"/variants.json")
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
				ind, err := faiss.ReadIndex(fmt.Sprintf("%s/indices/%d", base_dir, key), 0)
				return *ind, err
			}).
			EvictedFunc(func(key, value interface{}) {
				value.(faiss.Index).Delete()
			}).
			Build()
	} else {
		indices = make([]faiss.Index, len(schema.Partitions))
		for i, _ := range schema.Partitions {
			indices[i], err = faiss.ReadIndex(fmt.Sprintf("%s/indices/%d", base_dir, i), 0)
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
