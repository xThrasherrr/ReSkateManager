/* Included into every Lua source file by build.sh. Running WebAssembly is
   out of reach of the Go scheduler: until it calls into Go, nothing can stop
   it, and the garbage collector, which pauses every goroutine, waits for it.
   So Lua calls into Go now and then (pl_ask, every so many steps and at every
   call of a C function), and the host answers whether the call must stop: it
   ran out of time, or the plugin is stopping. Then it traps, which no pcall
   can catch. */
extern unsigned pl_steps;
void pl_ask(void);
#define pl_checkstop() ((void)((++pl_steps & 0x3FFF) == 0 && (pl_ask(), 0)))
