package redis

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/Desu-php/food/contracts/geo"
	"github.com/Desu-php/food/services/delivery/internal/application"
	"github.com/Desu-php/food/services/delivery/internal/domain"
	redis "github.com/redis/go-redis/v9"
)

// Shared Redis time, the high-water mark, rate limit, payload and GEO update are
// one operation across API replicas. Each courier payload has its own TTL.
var storeLocation = redis.NewScript(`
local t=redis.call('TIME')
local now=t[1]*1000000+tonumber(t[2])
local recorded=tonumber(ARGV[4])
local ttl=tonumber(ARGV[5])*1000
if recorded<=now-ttl or recorded>now+tonumber(ARGV[8]) then return -2 end
local previous=redis.call('HGET',KEYS[3],'recorded')
if previous and recorded<=tonumber(previous) then return -1 end
local last=redis.call('ZSCORE',KEYS[2],ARGV[1])
if last and tonumber(last)>now/1000-tonumber(ARGV[6]) then return 0 end
local expired=redis.call('ZRANGEBYSCORE',KEYS[2],'-inf',now/1000-tonumber(ARGV[5]),'LIMIT',0,1000)
for _,id in ipairs(expired) do
 redis.call('ZREM',KEYS[1],id)
 redis.call('ZREM',KEYS[2],id)
 redis.call('ZREM',KEYS[4],id)
end
if ARGV[9]=='1' then
 redis.call('GEOADD',KEYS[1],ARGV[2],ARGV[3],ARGV[1])
 redis.call('ZADD',KEYS[2],now/1000,ARGV[1])
else
 redis.call('ZREM',KEYS[1],ARGV[1])
 redis.call('ZADD',KEYS[2],now/1000,ARGV[1])
end
redis.call('ZADD',KEYS[4],recorded/1000,ARGV[1])
redis.call('PEXPIRE',KEYS[4],ARGV[5])
redis.call('PEXPIRE',KEYS[1],ARGV[5])
redis.call('PEXPIRE',KEYS[2],ARGV[5])
redis.call('HSET',KEYS[3],'recorded',ARGV[4],'received',string.format('%.0f',now),'body',ARGV[7])
redis.call('PEXPIRE',KEYS[3],ARGV[5])
return 1
`)

var latestLocation = redis.NewScript(`
local t=redis.call('TIME')
local now=t[1]*1000000+tonumber(t[2])
local values=redis.call('HMGET',KEYS[1],'recorded','received','body')
if not values[1] or not values[2] or not values[3] then return {} end
if tonumber(values[1])<=now-tonumber(ARGV[1])*1000 or tonumber(values[2])<=now-tonumber(ARGV[1])*1000 then return {} end
return {values[3],values[2]}
`)

func (s *Locations) locationKey(id int64) string {
	return s.key + ":location:" + strconv.FormatInt(id, 10)
}

func (s *Locations) Store(ctx context.Context, l domain.Location) error {
	if err := l.Validate(time.Now(), s.ttl, domain.MaxLocationAccuracyMeters); err != nil {
		return err
	}
	body, err := json.Marshal(l)
	if err != nil {
		return domain.ErrInvalid
	}
	searchable := "0"
	if (geo.Point{Latitude: l.Latitude, Longitude: l.Longitude}).Searchable() {
		searchable = "1"
	}
	n, err := storeLocation.Run(ctx, s.client, []string{s.key + ":geo", s.key + ":seen", s.locationKey(l.CourierID), s.key + ":recorded"},
		l.CourierID, l.Longitude, l.Latitude, l.RecordedAt.UnixMicro(), s.ttl.Milliseconds(), s.interval.Milliseconds(), body, domain.LocationClockSkew.Microseconds(), searchable).Int()
	if err != nil {
		return application.ErrLocationUnavailable
	}
	switch n {
	case -2:
		return domain.ErrInvalid
	case -1:
		return domain.ErrStaleLocation
	case 0:
		return application.ErrLocationRate
	}
	return nil
}

func (s *Locations) Latest(ctx context.Context, id int64) (domain.Location, bool, error) {
	var l domain.Location
	values, err := latestLocation.Run(ctx, s.client, []string{s.locationKey(id)}, s.ttl.Milliseconds()).StringSlice()
	if err != nil {
		return l, false, application.ErrLocationUnavailable
	}
	if len(values) == 0 {
		return l, false, nil
	}
	if len(values) != 2 || json.Unmarshal([]byte(values[0]), &l) != nil {
		return l, false, application.ErrLocationUnavailable
	}
	received, err := strconv.ParseInt(values[1], 10, 64)
	if err != nil {
		return l, false, application.ErrLocationUnavailable
	}
	l.ReceivedAt = time.UnixMicro(received).UTC()
	return l, true, nil
}
