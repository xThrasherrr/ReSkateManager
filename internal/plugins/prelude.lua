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
-- servers: "player.joined", "player.left", "chat", "map", "state",
-- "reskate" (a message from a ReSkate plugin on the server), or
-- "throwdown." and a kind ("throwdown.finished").
function on(event, fn) call("on", event, fn) end

-- command(word, fn) calls fn(e) each time a player types /word on one of the
-- plugin's servers: e.id and e.name are theirs, e.args the words after it
-- and e.text all of them. The word must be in commands in plugin.toml; a
-- server takes a new one as it next starts.
function command(word, fn) call("command", word, fn) end

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

-- api(prefix, names) is a table whose functions each call prefix.name.
local function api(prefix, names)
  local t = {}
  for _, name in ipairs(names) do
    local what = prefix .. "." .. name
    t[name] = function(...) return call(what, ...) end
  end
  return t
end

-- server.* reads and acts on the plugin's servers, each call naming one by
-- its ID first: an event's e.server is the server it happened on.
-- server.list(), server.info(id), server.players(id) and server.roles(id)
-- read; server.roles is each of the server's roles as {name, tag, color,
-- rank}, or nil and why. The rest are commands, sent a few a second after
-- the call returns, and each needs a permission the owner granted the plugin
-- on its card: server.say(id, text), server.msg(id, steamId, text),
-- server.kick(id, steamId [, minutes]), server.ban(id, steamId),
-- server.unban(id, steamId), server.teleport(id, steamIds, steamId or {x =,
-- y =, z =}), server.role(id, steamId, role or "none"),
-- server.makerole(id, {name =, tag =, color =, rank =}), which adds the role
-- or sets what's given of an existing one, and server.command(id, line).
-- Each returns true once queued, and calls a function given last with
-- whether it went through and the server's reply.
server = api("server", {"list", "info", "players", "roles", "say", "msg", "kick", "ban", "unban", "teleport", "role", "makerole", "command"})

-- store.* keeps the plugin's own values, through restarts: store.get(key),
-- store.set(key, value) (nil deletes it), store.delete(key), true when it
-- was there, and store.keys([prefix]), sorted. A value is nil, a boolean, a
-- number, a string or a table of those; a store holds 1 MB.
store = api("store", {"get", "set", "delete", "keys"})

-- playtime.total(steamId [, server]) is a player's seconds on the servers,
-- or on one, and playtime.sessions(steamId [, limit]) their latest visits,
-- newest first, each {server, joined_at, and left_at and reason once over}.
-- Both return nil and why when the database is busy.
playtime = api("playtime", {"total", "sessions"})

-- stats.* ranks players on boards. stats.define(board, {title =, unit =,
-- keep = "sum", "best" or "fastest", public =}) makes one of the plugin's,
-- and stats.add(board, server, steamId, n) gives a player n on it.
-- stats.top(board [, {server =, period =, limit =}]), stats.rank(board,
-- steamId [, {server =, period =}]) and stats.around(board, steamId [,
-- {server =, period =, limit =}]) read any board, "playtime" too: all the
-- servers unless one is named, over "all" time, the "month" or the "week"
-- (from Monday, on the manager's clock). Rows are {rank, id, name, value};
-- rank returns rank and value. Each returns nil and why when the board
-- can't be read.
stats = api("stats", {"define", "add", "top", "rank", "around"})

-- ui.card(server, steamId or "all", card [, fn]) shows a card at the top of
-- one player's screen, or everyone's: card is text, or {title =, lines =
-- {...}}. A server shows one card at a time, so it waits its turn, a minute
-- at most; it needs players.chat, and fn hears whether it went up, as with
-- server.say. Today's servers show it as one line, the title and lines
-- joined with " · ", for 6 to 15 seconds, and not while the player has chat
-- off or a menu open.
ui = api("ui", {"card"})

-- discord.post(webhook, card [, fn]) posts card to a Discord channel through
-- one of its webhooks: card is text, or {title =, lines = {...}}, shown as
-- written and pinging nobody. Posts go one at a time, waiting out Discord's
-- rate, 16 at most; it needs discord.post, and fn hears whether it went, as
-- with server.say.
discord = api("discord", {"post"})

-- reskate.send(server, plugin, data [, fn]) sends data, a table or a value,
-- to a ReSkate plugin on the server, named by its id as a "reskate" event's
-- e.plugin is: its console command "rsm <id>" gets it as JSON in base64,
-- about 3 KB of JSON at most. Messages go apart from server.*'s, ten a
-- second and 64 waiting at most; it needs reskate.send, and fn hears whether
-- the server ran it, as with server.say.
reskate = api("reskate", {"send"})

-- expose(name, fn) offers fn to the manager's other plugins: one's
-- call(plugin, name, args [, fn]) runs it as fn(e), with e.from that
-- plugin's ID and e.args a copy of args, and what it returns is the answer,
-- a copy too. Copies hold what JSON does, 8 tables deep at most. call
-- returns true once queued, or false and why when the plugin isn't running,
-- doesn't expose name or is too busy, and fn hears fn(true, answer), or
-- fn(false, why) when it failed. A plugin may have 64 calls waiting.
function expose(name, fn)
  if type(fn) ~= "function" then
    error("bad argument #2 to 'expose' (function expected, got " .. type(fn) .. ")", 2)
  end
  call("expose", name, function(e) call("answer", (fn(e))) end)
end
_ENV.call = function(...) return call("call", ...) end -- _ENV: call is the bridge's here

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
