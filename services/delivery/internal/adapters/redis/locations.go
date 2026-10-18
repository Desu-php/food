// Package redis implements an ephemeral candidate index, never assignment authority.
package redis

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/delivery/internal/application"
)

type Locations struct {
	client        *redis.Client
	key           string
	ttl, interval time.Duration
}

func NewLocations(client *redis.Client, key string, ttl, interval time.Duration) *Locations {
	return &Locations{client, key, ttl, interval}
}

// Redis TIME is shared by all API instances. Sorted-set timestamps make freshness
// and rate limiting atomic with GEOADD without one persistent GPS write.
var update = redis.NewScript(`
local t=redis.call('TIME')
local now=t[1]*1000+math.floor(t[2]/1000)
local expired=redis.call('ZRANGEBYSCORE',KEYS[2],'-inf',now-tonumber(ARGV[4]),'LIMIT',0,1000)
for _,id in ipairs(expired) do
 redis.call('ZREM',KEYS[1],id)
 redis.call('ZREM',KEYS[2],id)
end
local last=redis.call('ZSCORE',KEYS[2],ARGV[1])
if last and tonumber(last)>now-tonumber(ARGV[5]) then return 0 end
redis.call('GEOADD',KEYS[1],ARGV[2],ARGV[3],ARGV[1])
redis.call('ZADD',KEYS[2],now,ARGV[1])
redis.call('PEXPIRE',KEYS[1],ARGV[4])
redis.call('PEXPIRE',KEYS[2],ARGV[4])
return 1
`)
var nearest = redis.NewScript(`
local t=redis.call('TIME')
local now=t[1]*1000+math.floor(t[2]/1000)
local expired=redis.call('ZRANGEBYSCORE',KEYS[2],'-inf',now-tonumber(ARGV[4]),'LIMIT',0,1000)
for _,id in ipairs(expired) do
 redis.call('ZREM',KEYS[1],id)
 redis.call('ZREM',KEYS[2],id)
end
local result={}
local candidates=redis.call('GEOSEARCH',KEYS[1],'FROMLONLAT',ARGV[1],ARGV[2],'BYRADIUS',ARGV[3],'m','ASC','WITHDIST')
for _,v in ipairs(candidates) do
 local seen=redis.call('ZSCORE',KEYS[2],v[1])
 if seen and tonumber(seen)>now-tonumber(ARGV[4]) then
  table.insert(result,{v[1],v[2],tostring(tonumber(seen)+tonumber(ARGV[4]))})
 else
  redis.call('ZREM',KEYS[1],v[1])
 end
end
return result
`)

func (s *Locations) Update(ctx context.Context, id int64, p geo.Point) error {
	if id <= 0 || !p.Searchable() {
		return fmt.Errorf("invalid location")
	}
	n, err := update.Run(ctx, s.client, []string{s.key + ":geo", s.key + ":seen"}, id, p.Longitude, p.Latitude, s.ttl.Milliseconds(), s.interval.Milliseconds()).Int()
	if err != nil {
		return application.ErrLocationUnavailable
	}
	if n == 0 {
		return application.ErrLocationRate
	}
	return nil
}
func (s *Locations) Nearest(ctx context.Context, p geo.Point, radius float64) ([]application.Candidate, error) {
	values, err := nearest.Run(ctx, s.client, []string{s.key + ":geo", s.key + ":seen"}, p.Longitude, p.Latitude, radius, s.ttl.Milliseconds()).Slice()
	if err != nil {
		return nil, application.ErrLocationUnavailable
	}
	candidates := make([]application.Candidate, 0, len(values))
	for _, v := range values {
		row, ok := v.([]interface{})
		if !ok || len(row) != 3 {
			return nil, application.ErrLocationUnavailable
		}
		id, e1 := strconv.ParseInt(fmt.Sprint(row[0]), 10, 64)
		distance, e2 := strconv.ParseFloat(fmt.Sprint(row[1]), 64)
		expires, e3 := strconv.ParseInt(fmt.Sprint(row[2]), 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || id <= 0 {
			return nil, application.ErrLocationUnavailable
		}
		candidates = append(candidates, application.Candidate{CourierID: id, DistanceMeters: distance, ExpiresAt: time.UnixMilli(expires)})
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.DistanceMeters == b.DistanceMeters {
			return a.CourierID < b.CourierID
		}
		return a.DistanceMeters < b.DistanceMeters
	})
	return candidates, nil
}
