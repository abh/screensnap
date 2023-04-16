# Screensnap

A tool for generating screenshots of NTP Pool metrics.

# Usage

```
docker run \
    -p 8000:8000 -p 8001:8001 \
    -e upstream_base=https://www.ntppool.org/ \
    -ti harbor.ntppool.org/ntppool/screensnap
```

Then drive it from some other script that fetches URLs and does
something with the returned PNG.

```
curl http://localhost:8000/image/offset/216.239.35.12 -o out.png
```
