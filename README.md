# k8s-istio-control

generate k8s/istio config with tempalte and env. set a different namespace in env config to isolate each developer's the dev environment.
quick manage k8s resource such as start, stop, port, log ...

## env config

```
output_path: ./output

vars:
  run_env: TEST # PROD, DEV
  benvoy_hostnet: false
  namespace: ruifengyun
  stat_addr: monitor:20090
  std_level: verbose

skip_inject_service:

service:
  - proxy
  - backend
  - cache

must_deps:

high_priority_deps:

mid_priority_deps:

low_priority_deps:

service_group:
  biz:
  push:
  im:

disable:

enable:
  service:
  service_group:
```

## Control Usage

```
Usage of ./control [ option ] [ cmd args... ]:

[option]:
-env string
	env file
	(default: {pwd}/etc/test_env.yaml)
-tail int
	lines of recent log file to display
	(default: 150)
-since string
	only return logs newer than a relative duration like 5s, 2m, or 3h. Defaults to all logs
-h bool
	show help


[cmd argv]: (for all k8s resource)
# start all resource
./control start

# stop all resource
./control stop

# query all resource
./control status

# generate config by template and env
./control gen

# query nodePort
./control port

# query k8s service list
./control service

# query k8s pod list
./control pod


[cmd argv ...]:
./control log {service name}
./control start {service name}
./control stop {service name}
./control reload {service name}


```
