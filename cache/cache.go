package cache

import (
	"fmt"
	"sort"
	"time"

	"github.com/lomik/go-carbon/points"

	"github.com/Sirupsen/logrus"
)

type queue []*queueItem

func (v queue) Len() int           { return len(v) }
func (v queue) Swap(i, j int)      { v[i], v[j] = v[j], v[i] }
func (v queue) Less(i, j int) bool { return v[i].count < v[j].count }

// Settings ...
type Settings struct {
	MaxSize        int    // cache capacity (points)
	GraphPrefix    string // prefix for internal metrics
	InputCapacity  int    // input channel capacity
	OutputCapacity int    // output channel capacity
	QueryCapacity  int    // carbonlink query channel capacity
}

// SettingsQuery ...
type SettingsQuery struct {
	Settings  *Settings      // set new settings if != nil
	ReplyChan chan *Settings // result of query
}

// Cache stores and aggregate metrics in memory
type Cache struct {
	settings     *Settings
	data         map[string]*points.Points
	queue        queue
	inputChan    chan *points.Points // from receivers
	outputChan   chan *points.Points // to persisters
	queryChan    chan *Query         // from carbonlink
	settingsChan chan *SettingsQuery // get or set settings via query to cache worker
	exitChan     chan bool           // close for stop worker
	size         int                 // points count in data
	queryCnt     int                 // queries count in this checkpoint period
	overflowCnt  int                 // drop packages if cache full
}

// New create Cache instance and run in/out goroutine
func New() *Cache {
	settings := &Settings{
		MaxSize:        1000000,
		GraphPrefix:    "carbon.",
		InputCapacity:  51200,
		OutputCapacity: 1024,
		QueryCapacity:  16,
	}
	cache := &Cache{
		settings:    settings,
		data:        make(map[string]*points.Points, 0),
		queue:       make(queue, 0),
		exitChan:    make(chan bool),
		queryChan:   make(chan *Query, settings.QueryCapacity),
		size:        0,
		queryCnt:    0,
		overflowCnt: 0,
	}
	return cache
}

// Settings returns copy of cache settings object
func (c *Cache) Settings(newSettings *Settings) *Settings {
	// change for running cache
}

// Get any key/values pair from Cache
func (c *Cache) Get() *points.Points {
	for {
		size := len(c.queue)
		if size == 0 {
			break
		}
		cacheRecord := c.queue[size-1]
		c.queue = c.queue[:size-1]

		if values, ok := c.data[cacheRecord.metric]; ok {
			return values
		}
	}
	for _, values := range c.data {
		return values
	}
	return nil
}

// Remove key from cache
func (c *Cache) Remove(key string) {
	if value, exists := c.data[key]; exists {
		c.size -= len(value.Data)
		delete(c.data, key)
	}
}

// Pop return and remove next for save point from cache
func (c *Cache) Pop() *points.Points {
	v := c.Get()
	if v != nil {
		c.Remove(v.Metric)
	}
	return v
}

// Add points to cache
func (c *Cache) Add(p *points.Points) {
	if values, exists := c.data[p.Metric]; exists {
		values.Data = append(values.Data, p.Data...)
	} else {
		c.data[p.Metric] = p
	}
	c.size += len(p.Data)
}

// Size returns size
// func (c *Cache) Size() int {
// 	return c.size
// }

type queueItem struct {
	metric string
	count  int
}

// stat send internal statistics of cache
func (c *Cache) stat(metric string, value float64) {
	key := fmt.Sprintf("%scache.%s", c.graphPrefix, metric)
	c.Add(points.NowPoint(key, value))
	c.queue = append(c.queue, &queueItem{key, 1})
}

// doCheckpoint reorder save queue, add carbon metrics to queue
func (c *Cache) doCheckpoint() {
	start := time.Now()

	inputLenBeforeCheckpoint := len(c.inputChan)

	newQueue := make(queue, 0)

	for key, values := range c.data {
		newQueue = append(newQueue, &queueItem{key, len(values.Data)})
	}

	sort.Sort(newQueue)

	c.queue = newQueue

	inputLenAfterCheckpoint := len(c.inputChan)

	worktime := time.Now().Sub(start)

	c.stat("size", float64(c.size))
	c.stat("metrics", float64(len(c.data)))
	c.stat("queries", float64(c.queryCnt))
	c.stat("overflow", float64(c.overflowCnt))
	c.stat("checkpointTime", worktime.Seconds())
	c.stat("inputLenBeforeCheckpoint", float64(inputLenBeforeCheckpoint))
	c.stat("inputLenAfterCheckpoint", float64(inputLenAfterCheckpoint))

	logrus.WithFields(logrus.Fields{
		"time":                     worktime.String(),
		"size":                     c.size,
		"metrics":                  len(c.data),
		"queries":                  c.queryCnt,
		"overflow":                 c.overflowCnt,
		"inputLenBeforeCheckpoint": inputLenBeforeCheckpoint,
		"inputLenAfterCheckpoint":  inputLenAfterCheckpoint,
		"inputCapacity":            cap(c.inputChan),
	}).Info("[cache] doCheckpoint()")

	c.queryCnt = 0
	c.overflowCnt = 0
}

func (c *Cache) worker() {
	var values *points.Points
	var sendTo chan *points.Points

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		if values == nil {
			values = c.Pop()

			if values != nil {
				sendTo = c.outputChan
			} else {
				sendTo = nil
			}
		}

		select {
		case <-ticker.C: // checkpoint
			c.doCheckpoint()
		case query := <-c.queryChan: // carbonlink
			c.queryCnt++
			reply := NewReply()

			if values != nil && values.Metric == query.Metric {
				reply.Points = values.Copy()
			} else if v, ok := c.data[query.Metric]; ok {
				reply.Points = v.Copy()
			}

			query.ReplyChan <- reply
		case sendTo <- values: // to persister
			values = nil
		case msg := <-c.inputChan: // from receiver
			if c.maxSize == 0 || c.size < c.maxSize {
				c.Add(msg)
			} else {
				c.overflowCnt++
			}
		case <-c.exitChan: // exit
			break
		}
	}

}

// In returns input channel
func (c *Cache) In() chan *points.Points {
	if c.inputChan == nil {
		c.inputChan = make(chan *points.Points, c.inputCapacity)
	}
	return c.inputChan
}

// Out returns output channel
func (c *Cache) Out() chan *points.Points {
	return c.outputChan
}

// Query returns carbonlink query channel
func (c *Cache) Query() chan *Query {
	return c.queryChan
}

// Start worker
func (c *Cache) Start() {
	if c.outputChan == nil {
		c.outputChan = make(chan *points.Points, 1024)
	}
	go c.worker()
}

// Stop worker
func (c *Cache) Stop() {
	close(c.exitChan)
}
