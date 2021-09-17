# pmacct prometheus

## run
```
docker build -t pmacct-prometheus . && \
  docker run \
  --network host \
  --privileged \
  --rm \
  pmacct-prometheus
```
visit: http://localhost:9590/metrics


### pmacct example

```
docker run \
  --privileged \
	--network host \
	-v /tmp/metrics:/tmp/metrics \
	pmacct/pmacctd:latest \
	pmacctd \
		-r 1 \
		-c src_host,dst_host \
		-P memory \
		-p /tmp/metrics/collect.pipe \
		-P print \
		-O json
```
