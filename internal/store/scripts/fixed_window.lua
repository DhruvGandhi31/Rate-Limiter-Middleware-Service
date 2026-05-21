-- Fixed window counter rate limiter.
-- KEYS[1]: counter key
-- ARGV[1]: limit
-- ARGV[2]: window_seconds
-- ARGV[3]: now (unix ms)
-- Returns: {allowed (0|1), current_count, reset_at_ms}

local limit          = tonumber(ARGV[1])
local window_seconds = tonumber(ARGV[2])
local now_ms         = tonumber(ARGV[3])

local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('EXPIRE', KEYS[1], window_seconds)
end

local pttl = redis.call('PTTL', KEYS[1])
if pttl < 0 then
    pttl = window_seconds * 1000
    redis.call('EXPIRE', KEYS[1], window_seconds)
end
local reset_at_ms = now_ms + pttl

local allowed = 1
if count > limit then
    -- Roll back the increment so future calls reflect a stable count.
    redis.call('DECR', KEYS[1])
    count   = limit + 1
    allowed = 0
end

return { allowed, count, reset_at_ms }
