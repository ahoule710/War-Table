-- War Table Companion
-- Saves weekly progress for the War Table desktop app. Reads only out-of-combat
-- account data (Great Vault, Mythic+ history, raid lockouts). WoW writes this
-- to disk on /reload or logout, which is when the app picks it up.

local ADDON_NAME = ...
local GOLD = "|cffdcb65e"

WarTableDB = WarTableDB or {}

local frame = CreateFrame("Frame")
local pendingAfterCombat = false
local queued = false

local function safe(fn, ...)
  if type(fn) ~= "function" then return nil end
  local ok, a, b, c, d = pcall(fn, ...)
  if ok then return a, b, c, d end
  return nil
end

local vaultKinds = {}
if Enum and Enum.WeeklyRewardChestThresholdType then
  for name, value in pairs(Enum.WeeklyRewardChestThresholdType) do
    vaultKinds[value] = name
  end
end

-- GetNormalizedRealmName() can be nil early in login, so build the key from
-- GetRealmName() with spaces and dashes removed. That spelling never changes.
local function cleanRealm(realm)
  return (tostring(realm or "Realm"):gsub("[%s%-']", ""))
end

local function charKey()
  local name = UnitName("player") or "Unknown"
  return name .. "-" .. cleanRealm(GetRealmName())
end

-- Earlier versions could save one character under two keys. Drop the extras.
local function removeDuplicates(key, name, realm)
  local want = cleanRealm(realm):lower()
  for k, v in pairs(WarTableDB.chars) do
    if k ~= key and type(v) == "table" and v.name == name and cleanRealm(v.realm):lower() == want then
      WarTableDB.chars[k] = nil
    end
  end
end

local function collectVault(c)
  if not (C_WeeklyRewards and C_WeeklyRewards.GetActivities) then return end
  local acts = safe(C_WeeklyRewards.GetActivities)
  if type(acts) ~= "table" then return end
  local out = {}
  for _, a in ipairs(acts) do
    out[#out + 1] = {
      type = a.type,
      kind = vaultKinds[a.type],
      index = a.index,
      progress = a.progress,
      threshold = a.threshold,
      level = a.level,
    }
  end
  -- An empty list means the API isn't ready yet; keep the last good snapshot.
  if #out == 0 then return end
  c.vault = out
  c.vaultReady = safe(C_WeeklyRewards.HasAvailableRewards) and true or false
end

local function mapName(mapID)
  if not (mapID and C_ChallengeMode and C_ChallengeMode.GetMapUIInfo) then return nil end
  local name = safe(C_ChallengeMode.GetMapUIInfo, mapID)
  return name
end

local function collectMythicPlus(c)
  if C_MythicPlus and C_MythicPlus.GetRunHistory then
    local runs = safe(C_MythicPlus.GetRunHistory, false, true)
    if type(runs) == "table" then
      local out = {}
      for _, r in ipairs(runs) do
        out[#out + 1] = {
          map = mapName(r.mapChallengeModeID) or tostring(r.mapChallengeModeID or "?"),
          level = r.level,
          completed = r.completed and true or false,
        }
      end
      c.runs = out
    end
  end

  c.keystone = nil
  if C_MythicPlus and C_MythicPlus.GetOwnedKeystoneLevel then
    local level = safe(C_MythicPlus.GetOwnedKeystoneLevel)
    if level and level > 0 then
      local mapID = C_MythicPlus.GetOwnedKeystoneChallengeMapID and safe(C_MythicPlus.GetOwnedKeystoneChallengeMapID)
      c.keystone = { level = level, map = mapName(mapID) }
    end
  end

  if C_ChallengeMode and C_ChallengeMode.GetOverallDungeonScore then
    c.rating = safe(C_ChallengeMode.GetOverallDungeonScore)
  end
end

local function collectRaids(c)
  if not (GetNumSavedInstances and GetSavedInstanceInfo) then return end
  local now = time()
  local locks = {}
  for i = 1, (GetNumSavedInstances() or 0) do
    local name, _, reset, _, locked, _, _, isRaid, _, difficultyName, numEncounters, encounterProgress = GetSavedInstanceInfo(i)
    if locked and isRaid then
      locks[#locks + 1] = {
        name = name,
        difficulty = difficultyName,
        killed = encounterProgress or 0,
        total = numEncounters or 0,
        resetAt = now + (reset or 0),
      }
    end
  end
  c.raids = locks
end

local function collect()
  if InCombatLockdown() then
    pendingAfterCombat = true
    return
  end
  pendingAfterCombat = false

  WarTableDB.version = 1
  WarTableDB.chars = WarTableDB.chars or {}
  local key = charKey()
  local c = WarTableDB.chars[key] or {}
  WarTableDB.chars[key] = c

  c.name = UnitName("player")
  c.realm = GetRealmName()
  removeDuplicates(key, c.name, c.realm)
  local _, classFile = UnitClass("player")
  c.class = classFile
  c.level = UnitLevel("player")

  local specIndex = GetSpecialization and GetSpecialization()
  if specIndex then
    local _, specName, _, _, role = GetSpecializationInfo(specIndex)
    c.spec = specName
    c.role = role
  end

  local _, equipped = GetAverageItemLevel()
  if equipped and equipped > 0 then c.ilvl = math.floor(equipped * 10 + 0.5) / 10 end

  local secs = C_DateAndTime and safe(C_DateAndTime.GetSecondsUntilWeeklyReset)
  if secs then c.resetAt = time() + secs end

  collectVault(c)
  collectMythicPlus(c)
  collectRaids(c)
  c.updated = time()
end

local function queue(delay)
  if queued then return end
  queued = true
  C_Timer.After(delay or 2, function()
    queued = false
    collect()
  end)
end

local function summary()
  local c = WarTableDB.chars and WarTableDB.chars[charKey()]
  if not c then return "no data yet" end
  local parts = {}
  if c.vault then
    local best = {}
    for _, a in ipairs(c.vault) do
      local k = a.kind or tostring(a.type)
      if (a.progress or 0) >= (a.threshold or 1) then best[k] = (best[k] or 0) + 1 end
    end
    for k, n in pairs(best) do parts[#parts + 1] = k .. " " .. n .. "/3" end
  end
  if c.keystone then parts[#parts + 1] = "key +" .. c.keystone.level end
  return (#parts > 0) and table.concat(parts, ", ") or "nothing unlocked yet"
end

frame:SetScript("OnEvent", function(_, event, arg1)
  if event == "ADDON_LOADED" then
    if arg1 == ADDON_NAME then WarTableDB = WarTableDB or {} end
  elseif event == "PLAYER_LOGIN" then
    if RequestRaidInfo then RequestRaidInfo() end
    queue(5)
    -- Refresh once a minute out of combat, so the last snapshot before a
    -- reload or logout is recent. No snapshot is taken during logout itself:
    -- WoW returns blanks (item level 0, empty Vault) while it shuts down.
    if not frame.ticker then
      frame.ticker = C_Timer.NewTicker(60, function()
        if not InCombatLockdown() then collect() end
      end)
    end
  elseif event == "PLAYER_REGEN_ENABLED" then
    if pendingAfterCombat then queue(1) end
  elseif event == "CHALLENGE_MODE_COMPLETED" or event == "ENCOUNTER_END" then
    if RequestRaidInfo then RequestRaidInfo() end
    queue(4)
  else
    queue(2)
  end
end)

local events = {
  "ADDON_LOADED", "PLAYER_LOGIN", "PLAYER_REGEN_ENABLED",
  "WEEKLY_REWARDS_UPDATE", "CHALLENGE_MODE_COMPLETED", "ENCOUNTER_END",
  "UPDATE_INSTANCE_INFO", "PLAYER_SPECIALIZATION_CHANGED", "PLAYER_EQUIPMENT_CHANGED",
}
for _, ev in ipairs(events) do
  pcall(frame.RegisterEvent, frame, ev)
end

SLASH_WARTABLE1 = "/wartable"
SLASH_WARTABLE2 = "/wt"
SlashCmdList.WARTABLE = function()
  collect()
  print(GOLD .. "War Table|r: " .. summary() .. ". Type /reload to send it to the app now.")
end
