#!/bin/sh
# Builds ../lua.wasm: PUC Lua 5.4.8 and bridge.c, compiled to WebAssembly with
# wasi-sdk. Run by `task lua:wasm`, in a Linux container with this package's
# folder mounted at /w. The downloads are pinned by checksum, so a rebuild of
# the same files makes the same lua.wasm.
set -eu
LUA=lua-5.4.8
LUA_SHA=4f18ddae154e793e46eeab727c59ef1c0c0c2b744e7b94219710d76f530629ae
SDK=wasi-sdk-33.0-x86_64-linux
SDK_SHA=0ba8b5bfaeb2adf3f29bab5841d76cf5318ab8e1642ea195f88baba1abd47bce

mkdir -p /tmp/lua && cd /tmp/lua
curl -fsSLO https://www.lua.org/ftp/$LUA.tar.gz
curl -fsSLO https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-33/$SDK.tar.gz
printf '%s  %s\n%s  %s\n' $LUA_SHA $LUA.tar.gz $SDK_SHA $SDK.tar.gz | sha256sum -c
tar xzf $LUA.tar.gz
tar xzf $SDK.tar.gz

# The stop checks (plhooks.h), counted: at every jump and loop step, where
# Lua itself lets a signal break a loop; at every function entry, since tail
# calls loop without a jump; at every step of a pattern match and every
# comparison of a sort, which run no Lua. And an ask at every call of a C
# function, which may do a lot between steps.
S=$LUA/src
sed -i 's|^#define updatetrap(ci)  (trap = ci->u.l.trap)$|#define updatetrap(ci)  (pl_checkstop(), trap = ci->u.l.trap)|' $S/lvm.c
sed -i 's|^  trap = L->hookmask;$|  pl_checkstop(); trap = L->hookmask;|' $S/lvm.c
sed -i '/^  init: \/\* using goto to optimize tail recursion \*\/$/a pl_checkstop();' $S/lstrlib.c
sed -i '/^static int sort_comp (lua_State \*L, int a, int b) {$/a pl_checkstop();' $S/ltablib.c
sed -i '/^  checkstackGCp(L, LUA_MINSTACK, func);  \/\* ensure minimum stack size \*\/$/i pl_ask();' $S/ldo.c
test "$(grep -c pl_checkstop $S/lvm.c)" = 2
test "$(grep -c pl_checkstop $S/lstrlib.c)" = 1
test "$(grep -c pl_checkstop $S/ltablib.c)" = 1
test "$(grep -c pl_ask $S/ldo.c)" = 1

# Everything but the interpreter programs and the libraries plugins don't get
# (os, io, package, debug). Exceptions carry Lua's errors (setjmp/longjmp), in
# the encoding wazero runs.
SRC=$(ls $S/*.c | grep -vE '/(lua|luac|linit|loslib|liolib|loadlib|ldblib)\.c$')
$SDK/bin/clang --target=wasm32-wasip1 -O2 -mexec-model=reactor \
  -mllvm -wasm-enable-sjlj -mllvm -wasm-use-legacy-eh=false \
  -Dl_signalT=int -D_WASI_EMULATED_PROCESS_CLOCKS -include /w/wasm/plhooks.h \
  -ffile-prefix-map=/tmp/lua/= -I$S $SRC /w/wasm/bridge.c \
  -lsetjmp -lwasi-emulated-process-clocks -Wl,--strip-debug -o /w/lua.wasm
sha256sum /w/lua.wasm
