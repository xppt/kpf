kpf
===
K8s Port Forward tool.

One-shot port forward for a single command using dynamic local port.

Usage:
```
$ kpf [-n <ns>] <pod> <port> <prog>...
```

Specified `prog` will be exec-ed with already binded local port.
To receive the port value you need to use underscore (`_`) argument.
E.g.:
```
$ kpf my-redis-pod-0 6379 redis-cli -p _
```

Only the first and exact underscore argument will be replaced while starting the `prog`.
```
$ kpf my-redis-pod-0 6379 echo __ _ _
__ 43069 _
```
