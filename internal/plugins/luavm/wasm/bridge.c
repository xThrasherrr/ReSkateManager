/* The Lua side of package luavm: PUC Lua 5.4 built as a WASM reactor by
   build.sh, one instance per plugin. The host runs chunks, calls the functions
   a plugin handed it, and answers the plugin's calls. Values cross in the
   encoding codec.go reads and writes:

     'n' nil   'f' false   't' true   'i' int64   'd' float64
     's' u32 length, bytes            'F' u32 registry ref of a function
     'T' u32 pair count, then key and value of each pair

   A list of values (a call's arguments or results) is a u32 count, then the
   values. Numbers are little-endian. */
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include "lauxlib.h"
#include "lua.h"
#include "lualib.h"

#define IMPORT(n) __attribute__((import_module("rsm"), import_name(n)))
#define EXPORT(n) __attribute__((export_name(n)))

/* call asks the host to run one of its functions with the encoded arguments.
   It returns the length of the encoded results, or -(n + 1) for an error
   message n bytes long; take copies either one out. */
IMPORT("call") int32_t host_call(const char *name, int32_t nlen, const uint8_t *args, int32_t alen);
IMPORT("take") void host_take(uint8_t *dst);
/* fail reports why an exported entry point returned non-zero. */
IMPORT("fail") void host_fail(const char *msg, int32_t n);
/* ask is non-zero when the call must stop (plhooks.h). */
IMPORT("ask") int32_t host_ask(void);

#define MAXDEPTH 16
#define MAXPASS (1 << 20) /* the most one call may send the host */

unsigned pl_steps; /* see plhooks.h */

void pl_ask(void) {
  if (host_ask()) __builtin_trap();
}

static lua_State *L;

/* passerror raises an error at the plugin's line that made the call, past the
   prelude function that passed it on. */
static int passerror(lua_State *L, const char *fmt, const char *arg) {
  luaL_where(L, 2);
  lua_pushfstring(L, fmt, arg);
  lua_concat(L, 2);
  return lua_error(L);
}

/* ---- encoding, into one buffer: nothing runs Lua code while it fills ---- */

static uint8_t *tx;
static size_t txn, txcap;

static void put(lua_State *L, const void *p, size_t n) {
  if (txn + n > MAXPASS) passerror(L, "value too large to pass to the manager (over 1 MB)", NULL);
  if (txn + n > txcap) {
    size_t c = txcap ? txcap : 256;
    while (c < txn + n) c *= 2;
    uint8_t *q = realloc(tx, c);
    if (q == NULL) luaL_error(L, "not enough memory");
    tx = q, txcap = c;
  }
  memcpy(tx + txn, p, n);
  txn += n;
}

static void putbyte(lua_State *L, uint8_t b) { put(L, &b, 1); }

static void putu32(lua_State *L, uint32_t v) { put(L, &v, 4); }

static void encode(lua_State *L, int idx, int depth) {
  idx = lua_absindex(L, idx);
  switch (lua_type(L, idx)) {
    case LUA_TNIL:
      putbyte(L, 'n');
      break;
    case LUA_TBOOLEAN:
      putbyte(L, lua_toboolean(L, idx) ? 't' : 'f');
      break;
    case LUA_TNUMBER:
      if (lua_isinteger(L, idx)) {
        int64_t v = lua_tointeger(L, idx);
        putbyte(L, 'i');
        put(L, &v, 8);
      } else {
        double v = lua_tonumber(L, idx);
        putbyte(L, 'd');
        put(L, &v, 8);
      }
      break;
    case LUA_TSTRING: {
      size_t n;
      const char *s = lua_tolstring(L, idx, &n);
      putbyte(L, 's');
      putu32(L, (uint32_t)n);
      put(L, s, n);
      break;
    }
    case LUA_TFUNCTION:
      lua_pushvalue(L, idx);
      putbyte(L, 'F');
      putu32(L, (uint32_t)luaL_ref(L, LUA_REGISTRYINDEX));
      break;
    case LUA_TTABLE: {
      if (depth >= MAXDEPTH) passerror(L, "table nested too deeply to pass to the manager (a table inside itself?)", NULL);
      luaL_checkstack(L, 4, NULL);
      putbyte(L, 'T');
      size_t at = txn;
      uint32_t count = 0;
      putu32(L, 0);
      lua_pushnil(L);
      while (lua_next(L, idx)) {
        int kt = lua_type(L, -2);
        if (kt != LUA_TSTRING && kt != LUA_TNUMBER && kt != LUA_TBOOLEAN)
          passerror(L, "a table key that is a %s can't be passed to the manager", luaL_typename(L, -2));
        encode(L, -2, depth + 1);
        encode(L, -1, depth + 1);
        lua_pop(L, 1);
        count++;
      }
      memcpy(tx + at, &count, 4);
      break;
    }
    default:
      passerror(L, "a %s can't be passed to the manager", luaL_typename(L, idx));
  }
}

/* ---- decoding ---- */

typedef struct {
  const uint8_t *p, *end;
} Reader;

static void need(lua_State *L, Reader *r, size_t n) {
  if ((size_t)(r->end - r->p) < n) luaL_error(L, "the manager sent a value cut short");
}

static uint32_t getu32(lua_State *L, Reader *r) {
  uint32_t v;
  need(L, r, 4);
  memcpy(&v, r->p, 4);
  r->p += 4;
  return v;
}

static void decode(lua_State *L, Reader *r, int depth) {
  luaL_checkstack(L, 3, NULL);
  need(L, r, 1);
  switch (*r->p++) {
    case 'n':
      lua_pushnil(L);
      break;
    case 'f':
      lua_pushboolean(L, 0);
      break;
    case 't':
      lua_pushboolean(L, 1);
      break;
    case 'i': {
      int64_t v;
      need(L, r, 8);
      memcpy(&v, r->p, 8);
      r->p += 8;
      lua_pushinteger(L, v);
      break;
    }
    case 'd': {
      double v;
      need(L, r, 8);
      memcpy(&v, r->p, 8);
      r->p += 8;
      lua_pushnumber(L, v);
      break;
    }
    case 's': {
      uint32_t n = getu32(L, r);
      need(L, r, n);
      lua_pushlstring(L, (const char *)r->p, n);
      r->p += n;
      break;
    }
    case 'F':
      lua_rawgeti(L, LUA_REGISTRYINDEX, (lua_Integer)getu32(L, r));
      break;
    case 'T': {
      if (depth >= MAXDEPTH) luaL_error(L, "the manager sent a table nested too deeply");
      uint32_t n = getu32(L, r);
      lua_createtable(L, 0, n < 64 ? (int)n : 64);
      for (uint32_t i = 0; i < n; i++) {
        decode(L, r, depth + 1);
        decode(L, r, depth + 1);
        lua_rawset(L, -3);
      }
      break;
    }
    default:
      luaL_error(L, "the manager sent a value of an unknown kind");
  }
}

/* decodelist pushes a list of values and returns how many. */
static int decodelist(lua_State *L, Reader *r) {
  uint32_t n = getu32(L, r);
  if (n > 250) luaL_error(L, "the manager sent too many values");
  for (uint32_t i = 0; i < n; i++) decode(L, r, 0);
  return (int)n;
}

/* ---- what the prelude gets ---- */

static uint8_t *rx;
static size_t rxcap;

/* l_call(name, ...) runs one of the manager's functions. A finalizer can run
   while the results are decoded and make a call of its own, so they're read
   from a string of their own, not from rx. */
static int l_call(lua_State *L) {
  size_t nlen;
  const char *name = luaL_checklstring(L, 1, &nlen);
  int n = lua_gettop(L);
  txn = 0;
  putu32(L, (uint32_t)(n - 1));
  for (int i = 2; i <= n; i++) encode(L, i, 0);
  int32_t got = host_call(name, (int32_t)nlen, tx, (int32_t)txn);
  size_t len = got >= 0 ? (size_t)got : (size_t)(-(got + 1));
  if (len > rxcap) {
    uint8_t *q = realloc(rx, len);
    if (q == NULL) {
      host_take(NULL); /* drop it */
      luaL_error(L, "not enough memory");
    }
    rx = q, rxcap = len;
  }
  host_take(rx);
  if (got < 0) {
    luaL_where(L, 2); /* the plugin's line that made the call, past the prelude */
    lua_pushlstring(L, (const char *)rx, len);
    lua_concat(L, 2);
    return lua_error(L);
  }
  lua_settop(L, 0);
  lua_pushlstring(L, (const char *)rx, len);
  Reader r = {(const uint8_t *)lua_tostring(L, 1), NULL};
  r.end = r.p + len;
  return decodelist(L, &r);
}

/* l_loadsource(source, chunkname) compiles one of the plugin's files: text
   only, never precompiled chunks. */
static int l_loadsource(lua_State *L) {
  size_t n;
  const char *src = luaL_checklstring(L, 1, &n);
  const char *name = luaL_checkstring(L, 2);
  if (luaL_loadbufferx(L, src, n, name, "t") != LUA_OK) {
    luaL_pushfail(L);
    lua_insert(L, -2);
    return 2;
  }
  return 1;
}

/* ---- entry points ---- */

static int traceback(lua_State *L) {
  const char *msg = lua_tostring(L, 1);
  if (msg == NULL) msg = luaL_tolstring(L, 1, NULL);
  luaL_traceback(L, L, msg, 1);
  return 1;
}

/* finish reports a failed protected call's message and clears the stack. */
static int32_t finish(int status) {
  if (status != LUA_OK) {
    size_t n;
    const char *s = lua_tolstring(L, -1, &n);
    if (s == NULL) s = "(error object is not a string)", n = strlen(s);
    host_fail(s, (int32_t)n);
  }
  lua_settop(L, 0);
  return status;
}

static void openlib(const char *name, lua_CFunction f) {
  luaL_requiref(L, name, f, 1);
  lua_pop(L, 1);
}

EXPORT("pl_alloc") void *pl_alloc(int32_t n) { return malloc((size_t)n); }
EXPORT("pl_free") void pl_free(void *p) { free(p); }

/* pl_init makes the state: the safe libraries only, with no way to load code
   but the prelude's require. */
EXPORT("pl_init") int32_t pl_init(void) {
  L = luaL_newstate();
  if (L == NULL) return -1;
  lua_setwarnf(L, NULL, NULL); /* the prelude's warn writes to the plugin's log */
  openlib(LUA_GNAME, luaopen_base);
  openlib(LUA_STRLIBNAME, luaopen_string);
  openlib(LUA_TABLIBNAME, luaopen_table);
  openlib(LUA_MATHLIBNAME, luaopen_math);
  openlib(LUA_UTF8LIBNAME, luaopen_utf8);
  openlib(LUA_COLIBNAME, luaopen_coroutine);
  static const char *const gone[] = {"load", "loadfile", "dofile", NULL};
  for (const char *const *g = gone; *g; g++) {
    lua_pushnil(L);
    lua_setglobal(L, *g);
  }
  return 0;
}

static int run_prelude(lua_State *L) {
  size_t n;
  const char *src = lua_tolstring(L, 1, &n);
  if (luaL_loadbufferx(L, src, n, "=prelude", "t") != LUA_OK) return lua_error(L);
  lua_pushcfunction(L, l_call);
  lua_pushcfunction(L, l_loadsource);
  lua_call(L, 2, 0);
  return 0;
}

/* pl_prelude runs the manager's own chunk that builds the plugin API from
   l_call and l_loadsource, which it alone receives. */
EXPORT("pl_prelude") int32_t pl_prelude(const char *src, int32_t n) {
  lua_pushcfunction(L, traceback);
  lua_pushcfunction(L, run_prelude);
  lua_pushlstring(L, src, (size_t)n);
  return finish(lua_pcall(L, 1, 0, 1));
}

/* pl_run compiles and runs a chunk of the plugin's. */
EXPORT("pl_run") int32_t pl_run(const char *name, int32_t nlen, const char *src, int32_t n) {
  lua_pushcfunction(L, traceback);
  lua_pushlstring(L, name, (size_t)nlen);
  int status = luaL_loadbufferx(L, src, (size_t)n, lua_tostring(L, -1), "t");
  if (status == LUA_OK) status = lua_pcall(L, 0, 0, 1);
  return finish(status);
}

static int callref(lua_State *L) {
  Reader *r = lua_touserdata(L, 1);
  lua_rawgeti(L, LUA_REGISTRYINDEX, lua_tointeger(L, 2));
  int n = decodelist(L, r);
  lua_call(L, n, 0);
  return 0;
}

/* pl_callref calls a function the plugin handed the host, by its ref. */
EXPORT("pl_callref") int32_t pl_callref(int32_t ref, const uint8_t *args, int32_t n) {
  Reader r = {args, args + n};
  lua_pushcfunction(L, traceback);
  lua_pushcfunction(L, callref);
  lua_pushlightuserdata(L, &r);
  lua_pushinteger(L, ref);
  return finish(lua_pcall(L, 2, 0, 1));
}

/* pl_unref lets go of a function the host no longer needs. */
EXPORT("pl_unref") void pl_unref(int32_t ref) { luaL_unref(L, LUA_REGISTRYINDEX, ref); }

/* pl_memory is how many KB the plugin's Lua uses. */
EXPORT("pl_memory") int32_t pl_memory(void) { return lua_gc(L, LUA_GCCOUNT, 0); }
