package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	ustcCapacityKeyPrefix = "ustc:capacity:"
	ustcCapacityLease     = 120 * time.Second
)

var _ service.USTCCapacityCache = (*RPMCacheImpl)(nil)

// The metadata, active leases, and pending RPM reservations share a Redis
// Cluster hash tag. Every transition uses Redis TIME and one Lua invocation.
var ustcCapacityScript = redis.NewScript(`
local meta = KEYS[1]
local leases = KEYS[2]
local pending = KEYS[3]
local commits = KEYS[4]
local op = ARGV[1]
local id = ARGV[2]
local ticket_epoch = tonumber(ARGV[3]) or -1
local ticket_probe = tonumber(ARGV[4]) or 0
local rpm_arg = tonumber(ARGV[5]) or -1
local parallel_arg = tonumber(ARGV[6]) or -1
local status = tonumber(ARGV[7]) or -1
local remaining = tonumber(ARGV[8]) or -1
local response_limit = tonumber(ARGV[9]) or -1
local retry_ms = tonumber(ARGV[10]) or 0
local refund = tonumber(ARGV[11]) or 0
local lease_ms = tonumber(ARGV[12]) or 120000

local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)

local state = redis.call('HGET', meta, 'state')
local epoch = tonumber(redis.call('HGET', meta, 'epoch') or '0')
local used = tonumber(redis.call('HGET', meta, 'used') or '0')
local deadline = tonumber(redis.call('HGET', meta, 'deadline') or '0')
local waited = tonumber(redis.call('HGET', meta, 'waited') or '0')
local active = tonumber(redis.call('HGET', meta, 'active') or '0')

-- Expired leases release parallel and pending capacity. A sent RPM remains in
-- used; an unanswered probe instead enters a conservative cooldown.
local expired = redis.call('ZRANGEBYSCORE', leases, '-inf', now)
for _, expired_id in ipairs(expired) do
  redis.call('ZREM', leases, expired_id)
  redis.call('SREM', pending, expired_id)
  if redis.call('HGET', meta, 'probe') == expired_id then
    if redis.call('HGET', meta, 'c:' .. expired_id) then
      state = 'sync_wait'
      deadline = math.max(deadline, now + 60000)
      redis.call('HSET', meta, 'state', state, 'deadline', deadline)
    end
    redis.call('HDEL', meta, 'probe')
  end
  -- Keep send/feedback markers for a late response; only the active lease and
  -- pending reservation expire here.
  redis.call('HDEL', meta, 'seq:' .. expired_id)
end

-- Reading a cold key is observational. Return the virtual first-probe state
-- without creating Redis state; the first successful Reserve persists it.
if op == 'read' and redis.call('EXISTS', meta) == 0 then
  local cold_rpm = math.max(0, rpm_arg)
  local cold_parallel = math.max(0, parallel_arg)
  local cold_state = cold_rpm > 0 and 'verify_one' or 'ready'
  local cold_inflight = redis.call('ZCARD', leases)
  local cold_pending = redis.call('SCARD', pending)
  local cold_available = nil
  if cold_rpm > 0 then cold_available = cold_rpm - cold_pending end
  if cold_parallel > 0 then
    local parallel_available = cold_parallel - cold_inflight
    if cold_available == nil or parallel_available < cold_available then cold_available = parallel_available end
  end
  if cold_available == nil then cold_available = 0 end
  if cold_state == 'verify_one' then cold_available = math.min(1, math.max(0, cold_available)) end
  return {0, 1, 0, 0, cold_inflight, cold_pending, math.max(0, cold_available), 0, cold_state, cold_rpm, cold_parallel, 0, 0}
end

-- Limits are supplied by authoritative account metadata on reads, reserves,
-- and commits. A switch from unlimited to bounded starts with one probe.
if rpm_arg >= 0 and parallel_arg >= 0 then
  local old_rpm = redis.call('HGET', meta, 'rpm')
  if not old_rpm then
    if state ~= 'sync_wait' then
      local unknown_state = state ~= nil and state ~= 'ready' and state ~= 'verify_one'
      if unknown_state then
        epoch = math.max(epoch + 1, 1)
        state = 'verify_one'
      else
        epoch = math.max(epoch, 1)
      end
      used = 0
      if rpm_arg > 0 or unknown_state then
        state = 'verify_one'
        waited = 0
      else
        state = 'ready'
        waited = 0
      end
      active = 0
      deadline = 0
      if unknown_state then
        redis.call('HDEL', meta, 'probe', 'rollover_from', 'needs_anchor')
        redis.call('ZREMRANGEBYSCORE', commits, '-inf', '+inf')
      end
      redis.call('HSET', meta, 'epoch', epoch, 'used', used, 'state', state, 'waited', waited, 'active', active, 'deadline', deadline, 'cap', rpm_arg)
    end
  elseif tonumber(old_rpm) == 0 and rpm_arg > 0 and state ~= 'sync_wait' then
    epoch = epoch + 1
    used = 0
    state = 'verify_one'
    waited = 0
    active = 0
    deadline = 0
    redis.call('HDEL', meta, 'probe', 'needs_anchor')
    redis.call('HDEL', meta, 'rollover_from')
    redis.call('ZREMRANGEBYSCORE', commits, '-inf', '+inf')
    redis.call('HSET', meta, 'epoch', epoch, 'used', used, 'state', state, 'waited', waited, 'active', active, 'deadline', deadline, 'cap', rpm_arg)
  elseif rpm_arg == 0 and state ~= 'sync_wait' then
    state = 'ready'
    active = 0
    deadline = 0
    used = 0
    epoch = math.max(epoch, 1)
    redis.call('HDEL', meta, 'probe', 'needs_anchor')
    redis.call('HDEL', meta, 'rollover_from')
    redis.call('ZREMRANGEBYSCORE', commits, '-inf', '+inf')
    redis.call('HSET', meta, 'state', state, 'epoch', epoch, 'used', used, 'active', active, 'deadline', deadline, 'cap', 0)
  end
  redis.call('HSET', meta, 'rpm', rpm_arg, 'parallel', parallel_arg)
  local old_cap = tonumber(redis.call('HGET', meta, 'cap') or '-1')
  if old_cap < 0 then
    redis.call('HSET', meta, 'cap', rpm_arg)
  elseif rpm_arg > 0 then
    local effective = old_cap > 0 and math.min(old_cap, rpm_arg) or rpm_arg
    redis.call('HSET', meta, 'cap', effective)
  end
elseif not state then
  state = 'verify_one'
  epoch = math.max(epoch, 1)
  redis.call('HSET', meta, 'state', state, 'epoch', epoch, 'used', used, 'deadline', deadline, 'waited', waited, 'active', active)
end

state = redis.call('HGET', meta, 'state') or state or 'verify_one'
epoch = tonumber(redis.call('HGET', meta, 'epoch') or tostring(epoch))
used = tonumber(redis.call('HGET', meta, 'used') or tostring(used))
deadline = tonumber(redis.call('HGET', meta, 'deadline') or tostring(deadline))
waited = tonumber(redis.call('HGET', meta, 'waited') or tostring(waited))
active = tonumber(redis.call('HGET', meta, 'active') or tostring(active))
local rpm = tonumber(redis.call('HGET', meta, 'rpm') or '0')
local cap = tonumber(redis.call('HGET', meta, 'cap') or tostring(rpm))
local parallel = tonumber(redis.call('HGET', meta, 'parallel') or '0')

if state ~= 'ready' and state ~= 'sync_wait' and state ~= 'verify_one' then
  state = 'verify_one'
  epoch = math.max(epoch + 1, 1)
  used = 0
  deadline = 0
  waited = 0
  active = 0
  cap = math.max(0, rpm)
  redis.call('HDEL', meta, 'probe', 'rollover_from', 'needs_anchor')
  redis.call('ZREMRANGEBYSCORE', commits, '-inf', '+inf')
  redis.call('HSET', meta, 'state', state, 'epoch', epoch, 'used', used, 'deadline', deadline, 'waited', waited, 'active', active, 'cap', cap)
end

-- If every committed ticket in an unanchored mature window loses its lease
-- without a response, retain a full conservative minute from that loss.
if state == 'ready' and tonumber(redis.call('HGET', meta, 'needs_anchor') or '-1') == epoch then
  local unresolved = false
  local live_ids = redis.call('ZRANGE', leases, 0, -1)
  for _, live_id in ipairs(live_ids) do
    if tonumber(redis.call('HGET', meta, 'c:' .. live_id) or '-1') == epoch then
      unresolved = true
      break
    end
  end
  if not unresolved then
    redis.call('HDEL', meta, 'needs_anchor')
    deadline = math.max(deadline, now + 60000)
    active = 1
    state = 'sync_wait'
    redis.call('HSET', meta, 'state', state, 'deadline', deadline, 'active', active)
  end
end

-- A known fixed RPM window rolls directly into a fresh ready epoch after its
-- sixty seconds. A cooldown expiry instead requires one successful probe.
if state == 'ready' and active == 1 and deadline > 0 and now >= deadline then
  if tonumber(redis.call('HGET', meta, 'needs_anchor') or '-1') == epoch then
    -- A submitted request still needs a response to anchor this window.
    deadline = 0
    redis.call('HSET', meta, 'deadline', deadline)
  else
    local prior_epoch = epoch
    epoch = epoch + 1
    used = 0
    deadline = 0
    waited = 0
    active = 0
    cap = math.max(0, rpm)
    redis.call('HDEL', meta, 'probe', 'needs_anchor')
    redis.call('ZREMRANGEBYSCORE', commits, '-inf', '+inf')
    redis.call('HSET', meta, 'state', state, 'epoch', epoch, 'used', used, 'deadline', deadline, 'waited', waited, 'active', active, 'rollover_from', prior_epoch, 'cap', cap)
  end
elseif state == 'sync_wait' and deadline > 0 and now >= deadline then
  state = 'verify_one'
  epoch = epoch + 1
  used = 0
  deadline = 0
  waited = 1
  active = 0
  cap = math.max(0, rpm)
  redis.call('HDEL', meta, 'probe')
  redis.call('HDEL', meta, 'needs_anchor')
  redis.call('HDEL', meta, 'rollover_from')
  redis.call('ZREMRANGEBYSCORE', commits, '-inf', '+inf')
  redis.call('HSET', meta, 'state', state, 'epoch', epoch, 'used', used, 'deadline', deadline, 'waited', waited, 'active', active, 'cap', cap)
end

local inflight = redis.call('ZCARD', leases)
local pending_count = redis.call('SCARD', pending)

local function available_count()
  if state == 'sync_wait' then return 0 end
  local available = nil
  if cap > 0 then
    available = cap - used - pending_count
  end
  if parallel > 0 then
    local parallel_available = parallel - inflight
    if available == nil or parallel_available < available then
      available = parallel_available
    end
  end
  if available == nil then return 0 end
  available = math.max(0, available)
  if state == 'verify_one' then
    if redis.call('HGET', meta, 'probe') then return 0 end
    return math.min(1, available)
  end
  return available
end

local function reply(code, probe, seq)
  if redis.call('EXISTS', meta) == 1 then redis.call('PEXPIRE', meta, 86400000) end
  if redis.call('EXISTS', leases) == 1 then redis.call('PEXPIRE', leases, 86400000) end
  if redis.call('EXISTS', pending) == 1 then redis.call('PEXPIRE', pending, 86400000) end
  if redis.call('EXISTS', commits) == 1 then redis.call('PEXPIRE', commits, 86400000) end
  local cold = (probe == 1 and waited == 0) and 1 or 0
  return {code or 0, epoch, probe or 0, used, inflight, pending_count, available_count(), deadline, state, cap, parallel, seq or 0, cold}
end

if op == 'read' then
  -- read-only apart from expiry cleanup, lease cleanup, and authoritative limits.
elseif op == 'reserve' then
  if state == 'sync_wait' then
    -- wait for the cooldown, then exactly one request verifies recovery
  elseif parallel > 0 and inflight >= parallel then
    -- the active request leases still apply across RPM window rollover
  elseif cap > 0 and used + pending_count >= cap then
    -- pending reservations are capacity too
  else
    local probe = 0
    if state == 'verify_one' then
      if redis.call('HGET', meta, 'probe') then
        return reply(0, 0, 0)
      end
      probe = 1
    end
    local seq = redis.call('HINCRBY', meta, 'seq', 1)
    redis.call('ZADD', leases, now + lease_ms, id)
    redis.call('SADD', pending, id)
    redis.call('HSET', meta, 'seq:' .. id, seq)
    if probe == 1 then redis.call('HSET', meta, 'probe', id) end
    inflight = inflight + 1
    pending_count = pending_count + 1
    return reply(1, probe, seq)
  end
elseif op == 'commit' then
  local seq = tonumber(redis.call('HGET', meta, 'seq:' .. id) or '0')
  local held = redis.call('ZSCORE', leases, id)
  local is_pending = redis.call('SISMEMBER', pending, id)
  local rollover_from = tonumber(redis.call('HGET', meta, 'rollover_from') or '-1')
  local valid_epoch = ticket_epoch == epoch or (state == 'ready' and ticket_probe == 0 and ticket_epoch == rollover_from)
  local valid = held and is_pending and valid_epoch
  if ticket_probe == 1 then
    valid = valid and state == 'verify_one' and redis.call('HGET', meta, 'probe') == id
  else
    valid = valid and state == 'ready'
  end
  if parallel > 0 and inflight > parallel then valid = false end
  if cap > 0 and used + pending_count > cap then valid = false end
  if valid then
    redis.call('SREM', pending, id)
    pending_count = math.max(0, pending_count - 1)
    used = used + 1
    local commit_seq = redis.call('HINCRBY', meta, 'commit_seq', 1)
    redis.call('ZADD', commits, commit_seq, id)
    if active == 0 then
      active = 1
      deadline = now + 60000
      if state == 'ready' and cap > 0 then
        -- A mature fixed window is anchored by the first non-refunded
        -- response, not by a request that the provider rejects pre-RPM.
        redis.call('HSET', meta, 'needs_anchor', epoch)
      else
        redis.call('HSET', meta, 'first:' .. id, '1')
      end
    end
    redis.call('HSET', meta, 'used', used, 'active', active, 'deadline', deadline, 'c:' .. id, epoch, 'cs:' .. id, commit_seq)
    if not state then state = 'ready' end
    redis.call('HSET', meta, 'state', state)
    return reply(1, ticket_probe, seq)
  end
  return reply(0, 0, seq)
elseif op == 'release' then
  if redis.call('ZREM', leases, id) == 1 then
    if redis.call('SREM', pending, id) == 1 then
      pending_count = math.max(0, pending_count - 1)
    end
  end
  if redis.call('HGET', meta, 'probe') == id then
    if redis.call('HGET', meta, 'c:' .. id) then
      state = 'sync_wait'
      deadline = math.max(deadline, now + 60000)
      redis.call('HSET', meta, 'state', state, 'deadline', deadline)
    end
    redis.call('HDEL', meta, 'probe')
  end
  redis.call('HDEL', meta, 'c:' .. id, 'seq:' .. id, 'cs:' .. id, 'first:' .. id, 'refunded:' .. id)
elseif op == 'renew' then
  if redis.call('ZSCORE', leases, id) then
    redis.call('ZADD', leases, now + lease_ms, id)
    return reply(1, 0, 0)
  end
  return reply(0, 0, 0)
elseif op == 'observe' then
  local committed_epoch = tonumber(redis.call('HGET', meta, 'c:' .. id) or '-1')
  local commit_seq = tonumber(redis.call('HGET', meta, 'cs:' .. id) or '0')
  local current_probe = redis.call('HGET', meta, 'probe') == id
  if committed_epoch == ticket_epoch and ticket_epoch == epoch then
    local first_response = redis.call('HGET', meta, 'first:' .. id) == '1'
    local needs_anchor = tonumber(redis.call('HGET', meta, 'needs_anchor') or '-1') == epoch
    local refundable = status == 403 and refund == 1
    if response_limit > 0 then
      cap = cap > 0 and math.min(cap, response_limit) or response_limit
      redis.call('HSET', meta, 'cap', cap)
    end
    if needs_anchor and not refundable then
      if state == 'sync_wait' then
        deadline = math.max(deadline, now + 60000)
      else
        deadline = now + 60000
      end
      active = 1
      redis.call('HDEL', meta, 'needs_anchor')
      redis.call('HSET', meta, 'deadline', deadline, 'active', active)
    end
    if first_response and not refundable then
      deadline = math.max(deadline, now + 60000)
      redis.call('HSET', meta, 'deadline', deadline)
    end
    if first_response then redis.call('HDEL', meta, 'first:' .. id) end
    if status == 429 then
      if retry_ms <= 0 then retry_ms = 60000 end
      deadline = math.max(deadline, now + retry_ms)
      state = 'sync_wait'
      if current_probe then redis.call('HDEL', meta, 'probe') end
      redis.call('HSET', meta, 'state', state, 'deadline', deadline, 'active', 1)
    elseif status == 403 and refund == 1 and redis.call('HGET', meta, 'refunded:' .. id) ~= '1' then
      used = math.max(0, used - 1)
      redis.call('ZREM', commits, id)
      redis.call('HSET', meta, 'used', used, 'refunded:' .. id, '1')
      if needs_anchor and used == 0 then
        -- Refund of the only committed request leaves no real window. Keep
        -- any independent 429 cooldown, but let the next send start a window.
        redis.call('HDEL', meta, 'needs_anchor')
        active = 0
        if state == 'ready' then deadline = 0 end
        redis.call('HSET', meta, 'active', active, 'deadline', deadline)
      end
      if current_probe and ticket_probe == 1 then
        redis.call('HDEL', meta, 'probe')
        state = 'verify_one'
        active = 0
        deadline = 0
        waited = 0
        redis.call('HDEL', meta, 'first:' .. id)
        redis.call('HSET', meta, 'state', state, 'active', active, 'deadline', deadline, 'waited', waited)
      end
    elseif ticket_probe == 1 and current_probe and state == 'verify_one' then
      redis.call('HDEL', meta, 'probe')
      if status >= 200 and status < 300 then
        local observed_limit = response_limit > 0 and response_limit or rpm
        local later_commits = redis.call('ZCOUNT', commits, '(' .. tostring(commit_seq), '+inf')
        if remaining >= 0 and observed_limit > 0 then
          local observed = math.max(1, observed_limit - remaining) + later_commits
          used = math.max(used, observed)
          deadline = math.max(deadline, now + 60000)
          state = 'ready'
          waited = 0
        elseif waited == 1 then
          used = math.max(used, 1 + later_commits)
          deadline = math.max(deadline, now + 60000)
          state = 'ready'
          waited = 0
        else
          deadline = math.max(deadline, now + 60000)
          state = 'sync_wait'
          waited = 0
        end
        active = 1
      else
        if retry_ms <= 0 then retry_ms = 60000 end
        deadline = math.max(deadline, now + retry_ms)
        state = 'sync_wait'
        active = 1
        waited = 0
      end
      redis.call('HSET', meta, 'state', state, 'used', used, 'deadline', deadline, 'waited', waited, 'active', active)
    elseif status >= 200 and status < 300 and remaining >= 0 then
      local observed_limit = response_limit > 0 and response_limit or rpm
      if observed_limit > 0 then
        local later_commits = redis.call('ZCOUNT', commits, '(' .. tostring(commit_seq), '+inf')
        used = math.max(used, math.max(0, observed_limit - remaining) + later_commits)
        redis.call('HSET', meta, 'used', used)
      end
    elseif status == 0 and ticket_probe == 1 then
      if retry_ms <= 0 then retry_ms = 60000 end
      deadline = math.max(deadline, now + retry_ms)
      state = 'sync_wait'
      if current_probe then redis.call('HDEL', meta, 'probe') end
      redis.call('HSET', meta, 'state', state, 'deadline', deadline, 'active', 1)
    end
  end
elseif op == 'cooldown' then
  if retry_ms > 0 then
    deadline = math.max(deadline, now + retry_ms)
    state = 'sync_wait'
    if redis.call('HGET', meta, 'probe') then redis.call('HDEL', meta, 'probe') end
    redis.call('HSET', meta, 'state', state, 'deadline', deadline)
  end
else
  return redis.error_reply('unknown USTC capacity operation')
end

-- Refresh every extant key for at least 24h. Lease expiry is independently
-- enforced by the sorted-set score, so persisted state never frees a live RPM.
if redis.call('EXISTS', meta) == 1 then redis.call('PEXPIRE', meta, 86400000) end
if redis.call('EXISTS', leases) == 1 then redis.call('PEXPIRE', leases, 86400000) end
if redis.call('EXISTS', pending) == 1 then redis.call('PEXPIRE', pending, 86400000) end
if redis.call('EXISTS', commits) == 1 then redis.call('PEXPIRE', commits, 86400000) end

inflight = redis.call('ZCARD', leases)
pending_count = redis.call('SCARD', pending)
used = tonumber(redis.call('HGET', meta, 'used') or '0')
epoch = tonumber(redis.call('HGET', meta, 'epoch') or tostring(epoch))
deadline = tonumber(redis.call('HGET', meta, 'deadline') or '0')
state = redis.call('HGET', meta, 'state') or state or 'unknown'
rpm = tonumber(redis.call('HGET', meta, 'rpm') or '0')
cap = tonumber(redis.call('HGET', meta, 'cap') or tostring(rpm))
parallel = tonumber(redis.call('HGET', meta, 'parallel') or '0')

local result_code = 0
local result_probe = 0
local result_seq = 0
if op == 'reserve' then
  -- Successful reservations returned from their branch above.
  result_code = 0
elseif op == 'commit' then
  result_code = 0
elseif op == 'renew' then
  result_code = 1
end
return reply(result_code, result_probe, result_seq)
`)

func (c *RPMCacheImpl) ustcCapacityKeys(scope string) ([]string, error) {
	if strings.TrimSpace(scope) == "" {
		return nil, errors.New("ustc capacity scope must not be empty")
	}
	scopeHash := sha256.Sum256([]byte(scope))
	tag := hex.EncodeToString(scopeHash[:])
	root := ustcCapacityKeyPrefix + "{" + tag + "}"
	return []string{root + ":meta", root + ":leases", root + ":pending", root + ":commits"}, nil
}

func validateUSTCLimits(limits service.USTCLimits) error {
	if limits.RPM < 0 || limits.Parallel < 0 {
		return errors.New("ustc capacity limits must be nonnegative")
	}
	return nil
}

func newUSTCCapacityID() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("ustc capacity token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

type ustcCapacitySnapshot struct {
	code      int
	epoch     int64
	probe     bool
	used      int
	inFlight  int
	pending   int
	available int
	deadline  int64
	state     string
	rpm       int
	parallel  int
	seq       int64
	cold      bool
}

func parseUSTCCapacityReply(value interface{}) (ustcCapacitySnapshot, error) {
	items, err := ustcCapacityReplySlice(value)
	if err != nil {
		return ustcCapacitySnapshot{}, err
	}
	if len(items) < 13 {
		return ustcCapacitySnapshot{}, fmt.Errorf("unexpected USTC capacity response")
	}
	intAt := func(index int) (int64, error) {
		return ustcCapacityReplyInt64(items[index])
	}
	var out ustcCapacitySnapshot
	code, err := intAt(0)
	if err != nil {
		return ustcCapacitySnapshot{}, err
	}
	out.code = int(code)
	if out.epoch, err = intAt(1); err != nil {
		return ustcCapacitySnapshot{}, err
	}
	probe, err := intAt(2)
	if err != nil {
		return ustcCapacitySnapshot{}, err
	}
	out.probe = probe == 1
	values := []*int{&out.used, &out.inFlight, &out.pending, &out.available}
	for index, target := range values {
		parsed, parseErr := intAt(index + 3)
		if parseErr != nil {
			return ustcCapacitySnapshot{}, parseErr
		}
		*target = int(parsed)
	}
	if out.deadline, err = intAt(7); err != nil {
		return ustcCapacitySnapshot{}, err
	}
	if out.state, err = ustcCapacityReplyString(items[8]); err != nil {
		return ustcCapacitySnapshot{}, err
	}
	if parsed, parseErr := intAt(9); parseErr != nil {
		return ustcCapacitySnapshot{}, parseErr
	} else {
		out.rpm = int(parsed)
	}
	if parsed, parseErr := intAt(10); parseErr != nil {
		return ustcCapacitySnapshot{}, parseErr
	} else {
		out.parallel = int(parsed)
	}
	if out.seq, err = intAt(11); err != nil {
		return ustcCapacitySnapshot{}, err
	}
	cold, err := intAt(12)
	if err != nil {
		return ustcCapacitySnapshot{}, err
	}
	out.cold = cold == 1
	return out, nil
}

func ustcCapacityReplySlice(value interface{}) ([]interface{}, error) {
	items, ok := value.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected USTC capacity response type %T", value)
	}
	return items, nil
}

func ustcCapacityReplyInt64(value interface{}) (int64, error) {
	switch value := value.(type) {
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	case string:
		return strconv.ParseInt(value, 10, 64)
	case []byte:
		return strconv.ParseInt(string(value), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected USTC capacity number type %T", value)
	}
}

func ustcCapacityReplyString(value interface{}) (string, error) {
	switch value := value.(type) {
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	default:
		return "", fmt.Errorf("unexpected USTC capacity string type %T", value)
	}
}

func (c *RPMCacheImpl) runUSTCCapacity(ctx context.Context, scope, op, id string, epoch int64, probe bool, limits *service.USTCLimits, feedback *service.USTCFeedback, lease time.Duration) (ustcCapacitySnapshot, error) {
	keys, err := c.ustcCapacityKeys(scope)
	if err != nil {
		return ustcCapacitySnapshot{}, err
	}
	rpm, parallel := -1, -1
	if limits != nil {
		if err := validateUSTCLimits(*limits); err != nil {
			return ustcCapacitySnapshot{}, err
		}
		rpm, parallel = limits.RPM, limits.Parallel
	}
	status, remaining, responseLimit, retryMS, refund := -1, -1, -1, int64(0), 0
	if feedback != nil {
		status = feedback.StatusCode
		if feedback.Remaining != nil {
			remaining = *feedback.Remaining
		}
		if feedback.RPMLimit != nil {
			responseLimit = *feedback.RPMLimit
		}
		retryMS = ustcDurationMillis(feedback.RetryAfter)
		if feedback.Refund {
			refund = 1
		}
	}
	leaseMS := int64(ustcCapacityLease / time.Millisecond)
	if lease > 0 {
		leaseMS = lease.Milliseconds()
	}
	value, err := ustcCapacityScript.Run(ctx, c.rdb, keys,
		op, id, epoch, boolInt(probe), rpm, parallel, status, remaining,
		responseLimit, retryMS, refund, leaseMS).Result()
	if err != nil {
		return ustcCapacitySnapshot{}, fmt.Errorf("ustc capacity %s: %w", op, err)
	}
	return parseUSTCCapacityReply(value)
}

func ustcDurationMillis(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	milliseconds := duration.Milliseconds()
	if duration%time.Millisecond != 0 {
		milliseconds++
	}
	return milliseconds
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s ustcCapacitySnapshot) capacity() service.USTCCapacity {
	out := service.USTCCapacity{
		Used:      s.used,
		InFlight:  s.inFlight,
		Pending:   s.pending,
		Available: s.available,
		State:     s.state,
	}
	if s.rpm > 0 {
		limit := s.rpm
		out.RPMLimit = &limit
	}
	if s.parallel > 0 {
		limit := s.parallel
		out.ParallelLimit = &limit
	}
	if s.deadline > 0 {
		out.ResetAt = time.Unix(s.deadline/1000, (s.deadline%1000)*int64(time.Millisecond)).UTC()
	}
	return out
}

// USTCRead returns one Redis-time snapshot and performs lazy lease/window
// cleanup. Redis errors are returned so admission can fail closed.
func (c *RPMCacheImpl) USTCRead(ctx context.Context, scope string, limits service.USTCLimits) (service.USTCCapacity, error) {
	if err := validateUSTCLimits(limits); err != nil {
		return service.USTCCapacity{}, err
	}
	snapshot, err := c.runUSTCCapacity(ctx, scope, "read", "", 0, false, &limits, nil, 0)
	if err != nil {
		return service.USTCCapacity{}, err
	}
	return snapshot.capacity(), nil
}

// USTCReserve atomically holds a parallel lease and pending RPM capacity. In
// verify_one state it allows only one probe reservation.
func (c *RPMCacheImpl) USTCReserve(ctx context.Context, scope string, limits service.USTCLimits) (*service.USTCTicket, service.USTCCapacity, error) {
	if err := validateUSTCLimits(limits); err != nil {
		return nil, service.USTCCapacity{}, err
	}
	id, err := newUSTCCapacityID()
	if err != nil {
		return nil, service.USTCCapacity{}, err
	}
	snapshot, err := c.runUSTCCapacity(ctx, scope, "reserve", id, 0, false, &limits, nil, 0)
	if err != nil {
		return nil, service.USTCCapacity{}, err
	}
	if snapshot.code != 1 {
		return nil, snapshot.capacity(), nil
	}
	ticket := &service.USTCTicket{Scope: scope, ID: id, Epoch: snapshot.epoch, Probe: snapshot.probe, Cold: snapshot.cold}
	return ticket, snapshot.capacity(), nil
}

// USTCCommit revalidates the ticket's epoch, lease, and current capacity just
// before send. The epoch is updated on the ticket if the transition created it.
func (c *RPMCacheImpl) USTCCommit(ctx context.Context, ticket *service.USTCTicket, limits service.USTCLimits) (bool, error) {
	if err := validateUSTCLimits(limits); err != nil {
		return false, err
	}
	if ticket == nil || ticket.ID == "" {
		return false, errors.New("ustc capacity ticket is invalid")
	}
	snapshot, err := c.runUSTCCapacity(ctx, ticket.Scope, "commit", ticket.ID, ticket.Epoch, ticket.Probe, &limits, nil, 0)
	if err != nil {
		return false, err
	}
	if snapshot.code == 1 {
		ticket.Epoch = snapshot.epoch
		ticket.Probe = snapshot.probe
		ticket.Cold = snapshot.cold
		return true, nil
	}
	return false, nil
}

// USTCRelease is idempotent. It refunds only a still-pending reservation;
// committed RPM remains charged while its lease is removed.
func (c *RPMCacheImpl) USTCRelease(ctx context.Context, ticket *service.USTCTicket) error {
	if ticket == nil || ticket.ID == "" {
		return nil
	}
	_, err := c.runUSTCCapacity(ctx, ticket.Scope, "release", ticket.ID, ticket.Epoch, ticket.Probe, nil, nil, 0)
	return err
}

// USTCRenew extends only a live lease and never recreates an expired one.
func (c *RPMCacheImpl) USTCRenew(ctx context.Context, ticket *service.USTCTicket) error {
	if ticket == nil || ticket.ID == "" {
		return errors.New("ustc capacity ticket is invalid")
	}
	snapshot, err := c.runUSTCCapacity(ctx, ticket.Scope, "renew", ticket.ID, ticket.Epoch, ticket.Probe, nil, nil, ustcCapacityLease)
	if err != nil {
		return err
	}
	if snapshot.code != 1 {
		return errors.New("ustc capacity lease expired")
	}
	return nil
}

// USTCObserve applies response headers monotonically within the ticket epoch.
// Old responses cannot mutate a newer window or roll back its used count.
func (c *RPMCacheImpl) USTCObserve(ctx context.Context, ticket *service.USTCTicket, feedback service.USTCFeedback) error {
	if ticket == nil || ticket.ID == "" {
		return errors.New("ustc capacity ticket is invalid")
	}
	if feedback.Remaining != nil && *feedback.Remaining < 0 {
		return errors.New("ustc capacity remaining must be nonnegative")
	}
	if feedback.RPMLimit != nil && *feedback.RPMLimit < 0 {
		return errors.New("ustc capacity response limit must be nonnegative")
	}
	if feedback.RetryAfter < 0 {
		return errors.New("ustc capacity retry-after must be nonnegative")
	}
	_, err := c.runUSTCCapacity(ctx, ticket.Scope, "observe", ticket.ID, ticket.Epoch, ticket.Probe, nil, &feedback, 0)
	return err
}

// USTCCooldown applies a shared per-key 429/network cooldown and never shortens
// an existing deadline.
func (c *RPMCacheImpl) USTCCooldown(ctx context.Context, scope string, retryAfter time.Duration) error {
	if retryAfter < 0 {
		return errors.New("ustc capacity cooldown must be nonnegative")
	}
	if _, err := c.ustcCapacityKeys(scope); err != nil {
		return err
	}
	if retryAfter == 0 {
		return nil
	}
	_, err := c.runUSTCCapacity(ctx, scope, "cooldown", "", 0, false, nil, &service.USTCFeedback{RetryAfter: retryAfter}, 0)
	return err
}
