-- Token bucket rate limiter.
-- KEYS[1]: bucket hash key
-- ARGV[1]: capacity (max tokens)
-- ARGV[2]: refill_rate (tokens per second)
-- ARGV[3]: now (unix milliseconds)
-- ARGV[4]: requested tokens
-- ARGV[5]: ttl seconds
-- Returns: {allowed (0|1), remaining_tokens, reset_ms_until_full}

local capacity    = tonumber(ARGV[1])
local refill_rate = tonumber(ARGV[2])
local now         = tonumber(ARGV[3])
local requested   = tonumber(ARGV[4])
local ttl         = tonumber(ARGV[5])

local data        = redis.call('HMGET', KEYS[1], 'tokens', 'last_refill')
local tokens      = tonumber(data[1])
local last_refill = tonumber(data[2])

if tokens == nil then
    tokens      = capacity
    last_refill = now
end

local elapsed = (now - last_refill) / 1000.0
if elapsed > 0 then
    tokens = math.min(capacity, tokens + elapsed * refill_rate)
end
last_refill = now

local allowed = 0
if tokens >= requested then
    tokens  = tokens - requested
    allowed = 1
end

redis.call('HMSET', KEYS[1], 'tokens', tokens, 'last_refill', last_refill)
redis.call('EXPIRE', KEYS[1], ttl)

local reset_ms = 0
if tokens < capacity then
    reset_ms = math.ceil(((capacity - tokens) / refill_rate) * 1000)
end

return { allowed, math.floor(tokens), reset_ms }
