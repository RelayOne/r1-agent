# go-concurrency

> Concurrency patterns, race conditions, and goroutine lifecycle in Go

<!-- keywords: goroutine, mutex, channel, sync, race, deadlock, context, waitgroup, concurrent -->

## Critical Rules

1. **Every goroutine must have a termination path.** If you start `go func()`, there must be a `ctx.Done()`, channel close, or explicit return. Leaked goroutines are production memory bombs.

2. **Never hold a mutex across a channel send/receive.** This is the #1 cause of Go deadlocks. Release the lock, then send.

3. **Context cancellation must propagate.** If a function accepts `context.Context`, pass it to all sub-calls. Never shadow `ctx` with `context.Background()` inside a cancellable operation.

4. **`sync.WaitGroup.Add()` before `go func()`.** Adding inside the goroutine races with `Wait()`.

5. **Close channels from the sender side only.** Closing from receiver causes panics on concurrent sends.

6. **`select` with `default` is non-blocking.** Without `default`, `select` blocks. Know which you want.

## Common Gotchas

- **Loop variable capture:** `go func() { use(v) }()` captures `v` by reference. Use `go func(v T) { use(v) }(v)` or `v := v` before the goroutine.
- **Unbuffered channels block both sides.** `ch := make(chan T)` blocks sender until receiver reads. Use `make(chan T, 1)` if you need async.
- **`sync.Map` is NOT for general use.** Only use when keys are stable (append-only) or when each goroutine owns a disjoint set of keys. Otherwise use `sync.RWMutex` + `map`.
- **`time.After` leaks in loops.** Each call allocates a timer that isn't GC'd until it fires. Use `time.NewTimer` + `Reset()`.
- **`recover()` only catches panics in the same goroutine.** A panic in a spawned goroutine won't be caught by the parent's `defer recover()`.

## Patterns

### Worker Pool
```go
work := make(chan Task, len(tasks))
var wg sync.WaitGroup
for i := 0; i < workers; i++ {
    wg.Add(1)
    go func() {
        defer wg.Done()
        for task := range work {
            process(task)
        }
    }()
}
for _, t := range tasks { work <- t }
close(work)
wg.Wait()
```

### Timeout with Context
```go
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
select {
case result := <-doWork(ctx):
    return result, nil
case <-ctx.Done():
    return zero, ctx.Err()
}
```

### Safe Process Group Kill
```go
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
cmd.Cancel = func() error {
    return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
```
