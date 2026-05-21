-- Sliding window log rate limiter.
-- KEYS[1]: sorted set key
-- ARGV[1]: window_ms
-- ARGV[2]: limit
-- ARGV[3]: now (unix ms)
-- ARGV[4]: unique member id (e.g. now-rand) for the new entry
-- ARGV[5]: ttl seconds
-- Returns: {allowed (0|1), current_count, reset_at_ms}

local window_ms = tonumber(ARGV[1])
local limit     = tonumber(ARGV[2])
local now       = tonumber(ARGV[3])
local member    = ARGV[4]
local ttl       = tonumber(ARGV[5])

-- Trim entries outside the window.
local cutoff = now - window_ms
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', '(' .. cutoff)

local count   = redis.call('ZCARD', KEYS[1])
local allowed = 0
if count < limit then
    redis.call('ZADD', KEYS[1], now, member)
    count   = count + 1
    allowed = 1
end

redis.call('EXPIRE', KEYS[1], ttl)

-- Reset time = the moment the oldest entry leaves the window.
local oldest      = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
local reset_at_ms = now + window_ms
if #oldest >= 2 then
    reset_at_ms = tonumber(oldest[2]) + window_ms
end

return { allowed, count, reset_at_ms }
