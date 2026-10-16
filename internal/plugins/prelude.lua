-- The plugin API, plugin API 0 (a preview: it may still change). It runs
-- before a plugin's main.lua, and builds the API from the two functions the
-- bridge hands it alone: call asks the manager to do something, and
-- loadsource compiles one of the plugin's own files.
local call, loadsource = ...
local select, tostring, type, error = select, tostring, type, error
local concat = table.concat

local function line(...)
  local parts = {}
  for i = 1, select("#", ...) do
    parts[i] = tostring((select(i, ...)))
  end
  return concat(parts, " ")
end

-- on(event, fn) calls fn(e) each time event happens on one of the plugin's
-- servers: "player.joined", "player.left", "chat", "map", "state", or
-- "throwdown." and a kind ("throwdown.finished").
function on(event, fn) call("on", event, fn) end

-- after(seconds, fn) calls fn once, that long from now; every(seconds, fn)
-- calls it again and again. Both return the timer, for cancel.
function after(seconds, fn) return call("after", seconds, fn) end
function every(seconds, fn) return call("every", seconds, fn) end
function cancel(timer) return call("cancel", timer) end

-- log(...) writes a line to the plugin's log, on its card and in
-- data/plugins.log; warn(...) writes one marked as a warning.
function log(...) call("log", line(...)) end
function warn(...) call("warn", line(...)) end
print = log

-- server.* reads and acts on the plugin's servers, each call naming one by
-- its ID first: an event's e.server is the server it happened on.
-- server.list(), server.info(id) and server.players(id) read. The rest are
-- commands, sent a few a second after the call returns, and each needs a
-- permission the owner granted the plugin on its card: server.say(id,
-- text), server.msg(id, steamId, text), server.kick(id, steamId [,
-- minutes]), server.ban(id, steamId), server.unban(id, steamId),
-- server.teleport(id, steamIds, steamId or {x =, y =, z =}),
-- server.role(id, steamId, role or "none") and server.command(id, line).
-- Each returns true once queued, and calls a function given last with
-- whether it went through and the server's reply.
server = {}
for _, name in ipairs({"list", "info", "players", "say", "msg", "kick", "ban", "unban", "teleport", "role", "command"}) do
  local what = "server." .. name
  server[name] = function(...) return call(what, ...) end
end

-- require(name) runs one of the plugin's own files once and returns what it
-- returned: "lib.util" is lib/util.lua in the plugin's folder.
local loaded = {}
function require(name)
  if type(name) ~= "string" then
    error("bad argument #1 to 'require' (string expected, got " .. type(name) .. ")", 2)
  end
  local found = loaded[name]
  if found ~= nil then
    return found
  end
  local src, file = call("source", name)
  local chunk, err = loadsource(src, "=" .. file)
  if not chunk then
    error(err, 2)
  end
  loaded[name] = true -- a file that requires itself gets true, not a loop
  local result = chunk(name)
  if result == nil then
    result = true
  end
  loaded[name] = result
  return result
end
